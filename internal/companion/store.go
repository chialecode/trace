// Package companion implements the desktop TRACE mobile companion service:
// LAN/VPN HTTPS listener, TLS 1.3 identity, one-time QR & manual pairing with
// desktop confirmation, device registry (companion.json), read-only & control
// APIs, bounded WebSocket event hub, and MediaPath-whitelisted media/thumbnail
// delivery (docs/design/mobile.md §3–§6). Pure Go, zero MyGo dependency (INV-05).
package companion

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/companionapi"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/storage"
	"github.com/chialecode/trace/internal/uistate"
)

// On-disk file layout inside <DataDir> (docs/design/mobile.md §3).
const (
	StateFileName     = "companion.json"
	SchemaVersion     = 1
	TLSRelDir         = "companion/tls"
	CertFileName      = "cert.pem"
	KeyFileName       = "key.pem"
	ThumbsCacheRelDir = "cache/companion-thumbs"
)

// ErrDeviceNotFound reports an unknown paired device ID.
var ErrDeviceNotFound = errors.New("companion: 未找到该设备")

// ErrDeviceLimitReached reports that MaxDevices (5) are already paired.
var ErrDeviceLimitReached = errors.New("companion: 已配对设备数量已达上限（5 台），请先移除一台")

// DeviceRecord is one paired phone persisted in companion.json.
// TokenHash stores SHA-256 hex of the 32-byte device token; plaintext is never persisted.
type DeviceRecord struct {
	ID            string                          `json:"id"`
	Name          string                          `json:"name"`
	Model         string                          `json:"model,omitempty"`
	Platform      string                          `json:"platform"`
	Channel       string                          `json:"channel"`
	TokenHash     string                          `json:"tokenHash"`
	PairedAt      string                          `json:"pairedAt"`
	LastSeenAt    string                          `json:"lastSeenAt,omitempty"`
	LastAddr      string                          `json:"lastAddr,omitempty"`
	HomePositions map[string]uistate.HomePosition `json:"homePositions,omitempty"`
}

// StateFile is the on-disk JSON schema of <DataDir>/companion.json (version 1).
type StateFile struct {
	Version int            `json:"version"`
	Node    string         `json:"node"`
	Devices []DeviceRecord `json:"devices"`
}

// DeviceSummary is the sanitized desktop-UI representation of a paired device.
// It never exposes TokenHash or any secret.
type DeviceSummary struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	Model      string `json:"model,omitempty"`
	Platform   string `json:"platform"`
	Channel    string `json:"channel"`
	PairedAt   string `json:"pairedAt"`
	LastSeenAt string `json:"lastSeenAt,omitempty"`
	LastAddr   string `json:"lastAddr,omitempty"`
	Online     bool   `json:"online"`
}

// Store manages companion.json with atomic writes, 0600 permissions, and corrupt recovery.
type Store struct {
	mu            sync.Mutex
	path          string
	nodeID        string
	state         StateFile
	corruptBackup string
}

// OpenStore loads or initializes companion.json under dataDir.
// A corrupt JSON file is backed up via storage.BackupCorruptFile and reset to empty (NFR-DATA-001).
// A file with version > SchemaVersion returns storage.NewerDataError without modifying disk.
func OpenStore(dataDir, nodeName string) (*Store, error) {
	node, err := library.LoadOrCreateNode(dataDir, nodeName)
	if err != nil {
		return nil, fmt.Errorf("companion: load node: %w", err)
	}
	path := filepath.Join(dataDir, StateFileName)
	st := &Store{
		path:   path,
		nodeID: node.Node,
		state: StateFile{
			Version: SchemaVersion,
			Node:    node.Node,
			Devices: []DeviceRecord{},
		},
	}

	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return nil, fmt.Errorf("companion: read %s: %w", StateFileName, err)
	}

	var loaded StateFile
	if jErr := json.Unmarshal(raw, &loaded); jErr != nil {
		backup, bErr := storage.BackupCorruptFile(path)
		if bErr != nil {
			return nil, fmt.Errorf("companion: backup corrupt %s: %w", StateFileName, bErr)
		}
		st.corruptBackup = backup
		return st, nil
	}
	if loaded.Version > SchemaVersion {
		return nil, &storage.NewerDataError{
			File:      StateFileName,
			Found:     loaded.Version,
			Supported: SchemaVersion,
		}
	}
	if loaded.Version <= 0 {
		loaded.Version = SchemaVersion
	}
	if strings.TrimSpace(loaded.Node) == "" {
		loaded.Node = node.Node
	}
	loaded.Devices = normalizeDevices(loaded.Devices)
	st.state = loaded
	return st, nil
}

// CorruptBackup returns the backup path if companion.json was recovered from corruption.
func (s *Store) CorruptBackup() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.corruptBackup
}

// NodeID returns this installation's persistent node identifier.
func (s *Store) NodeID() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.nodeID
}

// Devices returns a copy of all paired devices.
func (s *Store) Devices() []DeviceRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]DeviceRecord, len(s.state.Devices))
	for i, d := range s.state.Devices {
		out[i] = cloneDevice(d)
	}
	return out
}

// Count returns the number of paired devices.
func (s *Store) Count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.state.Devices)
}

// AddDevice creates a new paired device record, generates a 32-byte random token,
// stores only its SHA-256 hash in companion.json, and returns the record and plaintext token once.
func (s *Store) AddDevice(name, model, platform, channel, addr string, now time.Time) (DeviceRecord, string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.state.Devices) >= companionapi.MaxDevices {
		return DeviceRecord{}, "", ErrDeviceLimitReached
	}
	ch, ok := companionapi.NormalizeChannel(channel)
	if !ok {
		return DeviceRecord{}, "", fmt.Errorf("companion: invalid channel %q", channel)
	}
	id, err := randomHex(16)
	if err != nil {
		return DeviceRecord{}, "", err
	}
	token, tokenHash, err := generateToken()
	if err != nil {
		return DeviceRecord{}, "", err
	}
	cleanName := sanitizeLabel(name, 64)
	if cleanName == "" {
		cleanName = "Android 手机"
	}
	ts := now.UTC().Format(time.RFC3339)
	rec := DeviceRecord{
		ID:         id,
		Name:       cleanName,
		Model:      sanitizeLabel(model, 64),
		Platform:   sanitizeLabel(platform, 32),
		Channel:    ch,
		TokenHash:  tokenHash,
		PairedAt:   ts,
		LastSeenAt: ts,
		LastAddr:   sanitizeLabel(addr, 64),
	}
	prev := s.state.Devices
	s.state.Devices = append(append([]DeviceRecord(nil), prev...), rec)
	if err := s.saveLocked(); err != nil {
		s.state.Devices = prev
		return DeviceRecord{}, "", err
	}
	return cloneDevice(rec), token, nil
}

// Authenticate verifies a bearer token in constant time across all paired devices.
// Returns the matched device record and true if valid.
func (s *Store) Authenticate(token string) (DeviceRecord, bool) {
	token = strings.TrimSpace(token)
	if token == "" {
		return DeviceRecord{}, false
	}
	gotHash := []byte(HashSecret(token))
	s.mu.Lock()
	defer s.mu.Unlock()
	matchIdx := -1
	for i, d := range s.state.Devices {
		if len(d.TokenHash) == len(gotHash) && subtle.ConstantTimeCompare([]byte(d.TokenHash), gotHash) == 1 {
			matchIdx = i
		}
	}
	if matchIdx < 0 {
		return DeviceRecord{}, false
	}
	return cloneDevice(s.state.Devices[matchIdx]), true
}

// TouchDevice updates LastSeenAt and LastAddr for deviceID (throttled writes optional).
func (s *Store) TouchDevice(deviceID, addr string, now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Devices {
		if s.state.Devices[i].ID != deviceID {
			continue
		}
		cleanAddr := sanitizeLabel(addr, 64)
		prevSeen, _ := time.Parse(time.RFC3339, s.state.Devices[i].LastSeenAt)
		// Avoid disk thrashing on every single media/thumbnail request: persist at most once per 30s
		// unless the remote address actually changed.
		if cleanAddr == s.state.Devices[i].LastAddr && !prevSeen.IsZero() && now.Sub(prevSeen) < 30*time.Second {
			return
		}
		s.state.Devices[i].LastSeenAt = now.UTC().Format(time.RFC3339)
		if cleanAddr != "" {
			s.state.Devices[i].LastAddr = cleanAddr
		}
		_ = s.saveLocked()
		return
	}
}

// RenameDevice updates a paired device's display name.
func (s *Store) RenameDevice(deviceID, newName string) (DeviceRecord, error) {
	clean := sanitizeLabel(newName, 64)
	if clean == "" {
		return DeviceRecord{}, errors.New("设备名称不能为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Devices {
		if s.state.Devices[i].ID == deviceID {
			old := s.state.Devices[i].Name
			s.state.Devices[i].Name = clean
			if err := s.saveLocked(); err != nil {
				s.state.Devices[i].Name = old
				return DeviceRecord{}, err
			}
			return cloneDevice(s.state.Devices[i]), nil
		}
	}
	return DeviceRecord{}, ErrDeviceNotFound
}

// RevokeDevice removes a paired device immediately from companion.json.
func (s *Store) RevokeDevice(deviceID string) (DeviceRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	idx := -1
	for i, d := range s.state.Devices {
		if d.ID == deviceID {
			idx = i
			break
		}
	}
	if idx < 0 {
		return DeviceRecord{}, ErrDeviceNotFound
	}
	removed := cloneDevice(s.state.Devices[idx])
	prev := s.state.Devices
	next := make([]DeviceRecord, 0, len(prev)-1)
	next = append(next, prev[:idx]...)
	next = append(next, prev[idx+1:]...)
	s.state.Devices = next
	if err := s.saveLocked(); err != nil {
		s.state.Devices = prev
		return DeviceRecord{}, err
	}
	return removed, nil
}

// ClearDevices removes all paired devices (used on identity reset).
func (s *Store) ClearDevices() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	prev := s.state.Devices
	s.state.Devices = []DeviceRecord{}
	if err := s.saveLocked(); err != nil {
		s.state.Devices = prev
		return err
	}
	return nil
}

// HomePositions returns a copy of the saved home positions for deviceID.
func (s *Store) HomePositions(deviceID string) (map[string]companionapi.HomePosition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, d := range s.state.Devices {
		if d.ID == deviceID {
			out := make(map[string]companionapi.HomePosition, len(d.HomePositions))
			for siteKey, pos := range d.HomePositions {
				out[siteKey] = companionapi.HomePosition{
					Site:     siteKey,
					Key:      pos.Key,
					PubTs:    pos.PubTs,
					OffsetPx: pos.OffsetPx,
				}
			}
			return out, nil
		}
	}
	return nil, ErrDeviceNotFound
}

// SaveHomePosition updates one site filter's home reading position for deviceID.
func (s *Store) SaveHomePosition(deviceID, siteFilter string, pos companionapi.HomePosition) error {
	siteKey := normalizePositionSite(siteFilter)
	if siteKey == "" {
		return fmt.Errorf("不支持的站点筛选：%q", siteFilter)
	}
	offset := pos.OffsetPx
	if offset < 0 {
		offset = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.state.Devices {
		if s.state.Devices[i].ID != deviceID {
			continue
		}
		prev := clonePositions(s.state.Devices[i].HomePositions)
		if s.state.Devices[i].HomePositions == nil {
			s.state.Devices[i].HomePositions = make(map[string]uistate.HomePosition)
		}
		cleanKey := strings.TrimSpace(pos.Key)
		if cleanKey == "" && pos.PubTs <= 0 {
			delete(s.state.Devices[i].HomePositions, siteKey)
		} else {
			s.state.Devices[i].HomePositions[siteKey] = uistate.HomePosition{
				Key:      cleanKey,
				PubTs:    pos.PubTs,
				OffsetPx: offset,
			}
		}
		if err := s.saveLocked(); err != nil {
			s.state.Devices[i].HomePositions = prev
			return err
		}
		return nil
	}
	return ErrDeviceNotFound
}

func (s *Store) saveLocked() error {
	s.state.Version = SchemaVersion
	s.state.Node = s.nodeID
	if s.state.Devices == nil {
		s.state.Devices = []DeviceRecord{}
	}
	return storage.WriteJSONAtomic(s.path, s.state, 0o600)
}

func normalizeDevices(in []DeviceRecord) []DeviceRecord {
	out := make([]DeviceRecord, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, d := range in {
		d.ID = strings.TrimSpace(d.ID)
		d.TokenHash = strings.ToLower(strings.TrimSpace(d.TokenHash))
		if d.ID == "" || len(d.TokenHash) != 64 || seen[d.ID] {
			continue
		}
		seen[d.ID] = true
		d.Name = sanitizeLabel(d.Name, 64)
		if d.Name == "" {
			d.Name = "Android 手机"
		}
		d.Model = sanitizeLabel(d.Model, 64)
		d.Platform = sanitizeLabel(d.Platform, 32)
		if ch, ok := companionapi.NormalizeChannel(d.Channel); ok {
			d.Channel = ch
		}
		if len(d.HomePositions) > 0 {
			clean := make(map[string]uistate.HomePosition, len(d.HomePositions))
			for k, p := range d.HomePositions {
				sk := normalizePositionSite(k)
				if sk == "" || (strings.TrimSpace(p.Key) == "" && p.PubTs <= 0) {
					continue
				}
				if p.OffsetPx < 0 {
					p.OffsetPx = 0
				}
				clean[sk] = p
			}
			d.HomePositions = clean
		}
		out = append(out, d)
		if len(out) >= companionapi.MaxDevices {
			break
		}
	}
	return out
}

func normalizePositionSite(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", "all":
		return "all"
	case "bilibili", "douyin", "weibo", "x":
		return strings.ToLower(strings.TrimSpace(raw))
	default:
		return ""
	}
}

func cloneDevice(d DeviceRecord) DeviceRecord {
	d.HomePositions = clonePositions(d.HomePositions)
	return d
}

func clonePositions(in map[string]uistate.HomePosition) map[string]uistate.HomePosition {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]uistate.HomePosition, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

// HashSecret returns lowercase hex SHA-256 of a secret token or pairSecret.
func HashSecret(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}

func generateToken() (token, hash string, err error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(raw[:])
	return token, HashSecret(token), nil
}

func randomHex(nBytes int) (string, error) {
	buf := make([]byte, nBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

func sanitizeLabel(s string, maxRunes int) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	b.Grow(len(s))
	n := 0
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			continue
		}
		if n >= maxRunes {
			break
		}
		b.WriteRune(r)
		n++
	}
	return strings.TrimSpace(b.String())
}
