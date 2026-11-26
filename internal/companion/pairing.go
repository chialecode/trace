package companion

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"math/big"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/companionapi"
)

// PairingSessionInfo is shown in the desktop "添加手机" dialog (docs/ui.md §3.12).
type PairingSessionInfo struct {
	Payload      string   `json:"payload"`
	ShortCode    string   `json:"shortCode"`
	IdentityCode string   `json:"identityCode"`
	Addrs        []string `json:"addrs"`
	Port         int      `json:"port"`
	ExpiresAt    string   `json:"expiresAt"`
}

// PendingPairInfo describes an incoming phone waiting for desktop user confirmation.
type PendingPairInfo struct {
	PairID     string `json:"pairId"`
	DeviceName string `json:"deviceName"`
	Model      string `json:"model,omitempty"`
	Platform   string `json:"platform"`
	RemoteAddr string `json:"remoteAddr,omitempty"`
	ExpiresAt  string `json:"expiresAt"`
}

type activePairCode struct {
	code          string // 128-bit hex for QR
	shortCode     string // 8-char unambiguous uppercase (no dash)
	shortCodeErrs int
	shortCodeDead bool
	expiresAt     time.Time
	payload       string
	addrs         []string
	port          int
	identityCode  string
}

type pendingPairRecord struct {
	pairID         string
	pairSecretHash string
	deviceName     string
	model          string
	platform       string
	channel        string
	remoteAddr     string
	createdAt      time.Time
	expiresAt      time.Time
	status         string // pending, approved, rejected, expired
	deviceID       string
	token          string // populated on approval, cleared on first successful poll
	done           chan struct{}
	closed         bool
}

type pairingManager struct {
	mu          sync.Mutex
	now         func() time.Time
	active      *activePairCode
	pending     map[string]*pendingPairRecord
	onStateDiff func()
}

func newPairingManager(now func() time.Time, onStateDiff func()) *pairingManager {
	if now == nil {
		now = time.Now
	}
	return &pairingManager{
		now:         now,
		pending:     make(map[string]*pendingPairRecord),
		onStateDiff: onStateDiff,
	}
}

// Start creates a fresh one-time QR + short pairing code valid for 5 minutes.
// Only one pairing code is active at any time (docs/design/mobile.md §4.1).
func (p *pairingManager) Start(nodeID, nodeName, channel, fp, identityCode string, port int, addrs []string) (PairingSessionInfo, error) {
	code, err := randomHex(16) // 128-bit random
	if err != nil {
		return PairingSessionInfo{}, err
	}
	shortCode, err := generateShortCode()
	if err != nil {
		return PairingSessionInfo{}, err
	}
	payload, err := companionapi.EncodePairPayload(companionapi.PairPayload{
		Version:     companionapi.PairPayloadVersion,
		Node:        nodeID,
		Name:        nodeName,
		Channel:     channel,
		Port:        port,
		Addrs:       addrs,
		Fingerprint: fp,
		Code:        code,
	})
	if err != nil {
		return PairingSessionInfo{}, err
	}
	expiresAt := p.now().Add(companionapi.PairCodeTTL)

	p.mu.Lock()
	p.active = &activePairCode{
		code:         code,
		shortCode:    shortCode,
		expiresAt:    expiresAt,
		payload:      payload,
		addrs:        append([]string(nil), addrs...),
		port:         port,
		identityCode: identityCode,
	}
	onDiff := p.onStateDiff
	p.mu.Unlock()
	if onDiff != nil {
		onDiff()
	}
	return PairingSessionInfo{
		Payload:      payload,
		ShortCode:    companionapi.FormatShortCode(shortCode),
		IdentityCode: identityCode,
		Addrs:        append([]string(nil), addrs...),
		Port:         port,
		ExpiresAt:    expiresAt.UTC().Format(time.RFC3339),
	}, nil
}

// ActiveSession returns the current non-expired pairing session, if any.
func (p *pairingManager) ActiveSession() *PairingSessionInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active == nil || !p.now().Before(p.active.expiresAt) {
		p.active = nil
		return nil
	}
	info := PairingSessionInfo{
		Payload:      p.active.payload,
		ShortCode:    companionapi.FormatShortCode(p.active.shortCode),
		IdentityCode: p.active.identityCode,
		Addrs:        append([]string(nil), p.active.addrs...),
		Port:         p.active.port,
		ExpiresAt:    p.active.expiresAt.UTC().Format(time.RFC3339),
	}
	if p.active.shortCodeDead {
		info.ShortCode = ""
	}
	return &info
}

// CancelSession invalidates any active QR/short pairing code.
func (p *pairingManager) CancelSession() {
	p.mu.Lock()
	p.active = nil
	onDiff := p.onStateDiff
	p.mu.Unlock()
	if onDiff != nil {
		onDiff()
	}
}

// ConsumeCode validates rawCode against the active QR code or short code and
// immediately invalidates the active code on match (one-time use).
// On mismatch of an 8-char short code candidate, increments the short-code error
// counter and invalidates the short code after 5 failed attempts (design §4.2).
func (p *pairingManager) ConsumeCode(rawCode string) error {
	trimmed := strings.TrimSpace(rawCode)
	if trimmed == "" {
		return errors.New("配对码无效或已过期")
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if p.active == nil || !now.Before(p.active.expiresAt) {
		p.active = nil
		return errors.New("配对码已过期，请在电脑端重新生成")
	}

	// Check 128-bit QR code in constant time.
	if len(trimmed) == len(p.active.code) && subtle.ConstantTimeCompare([]byte(trimmed), []byte(p.active.code)) == 1 {
		p.active = nil
		return nil
	}

	// Check 8-char manual short code in constant time.
	if !p.active.shortCodeDead {
		if normShort, ok := companionapi.NormalizeShortCode(trimmed); ok {
			if len(normShort) == len(p.active.shortCode) && subtle.ConstantTimeCompare([]byte(normShort), []byte(p.active.shortCode)) == 1 {
				p.active = nil
				return nil
			}
		}
		p.active.shortCodeErrs++
		if p.active.shortCodeErrs >= companionapi.PairShortCodeMaxErrors {
			p.active.shortCodeDead = true
		}
	}
	return errors.New("配对码不正确或已失效")
}

// CreatePending records a validated pairing request awaiting desktop confirmation (2-minute TTL).
func (p *pairingManager) CreatePending(req companionapi.PairRequest, remoteAddr string) (PendingPairInfo, error) {
	secret := strings.TrimSpace(req.PairSecret)
	if len(secret) < 16 {
		return PendingPairInfo{}, errors.New("缺少有效的配对密钥")
	}
	pairID, err := randomHex(16) // 128-bit random
	if err != nil {
		return PendingPairInfo{}, err
	}
	now := p.now()
	expiresAt := now.Add(companionapi.PairConfirmTTL)
	name := sanitizeLabel(req.DeviceName, 64)
	if name == "" {
		name = "Android 手机"
	}
	rec := &pendingPairRecord{
		pairID:         pairID,
		pairSecretHash: HashSecret(secret),
		deviceName:     name,
		model:          sanitizeLabel(req.Model, 64),
		platform:       sanitizeLabel(req.Platform, 32),
		channel:        req.Channel,
		remoteAddr:     sanitizeLabel(remoteAddr, 64),
		createdAt:      now,
		expiresAt:      expiresAt,
		status:         companionapi.PairStatusPending,
		done:           make(chan struct{}),
	}

	p.mu.Lock()
	p.pruneExpiredLocked(now)
	p.pending[pairID] = rec
	onDiff := p.onStateDiff
	p.mu.Unlock()
	if onDiff != nil {
		onDiff()
	}

	return PendingPairInfo{
		PairID:     pairID,
		DeviceName: rec.deviceName,
		Model:      rec.model,
		Platform:   rec.platform,
		RemoteAddr: rec.remoteAddr,
		ExpiresAt:  expiresAt.UTC().Format(time.RFC3339),
	}, nil
}

// PendingConfirmations lists all currently active pairing requests waiting on the desktop user.
func (p *pairingManager) PendingConfirmations() []PendingPairInfo {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	p.pruneExpiredLocked(now)
	out := make([]PendingPairInfo, 0, len(p.pending))
	for _, rec := range p.pending {
		if rec.status == companionapi.PairStatusPending {
			out = append(out, PendingPairInfo{
				PairID:     rec.pairID,
				DeviceName: rec.deviceName,
				Model:      rec.model,
				Platform:   rec.platform,
				RemoteAddr: rec.remoteAddr,
				ExpiresAt:  rec.expiresAt.UTC().Format(time.RFC3339),
			})
		}
	}
	return out
}

// Approve confirms a pending pairing request, creating the device record & one-time token.
func (p *pairingManager) Approve(pairID string, store *Store) (DeviceRecord, error) {
	p.mu.Lock()
	now := p.now()
	p.pruneExpiredLocked(now)
	rec, ok := p.pending[pairID]
	if !ok || rec.status != companionapi.PairStatusPending {
		p.mu.Unlock()
		return DeviceRecord{}, errors.New("配对请求不存在或已过期")
	}
	devName, model, platform, ch, addr := rec.deviceName, rec.model, rec.platform, rec.channel, rec.remoteAddr
	p.mu.Unlock()

	dev, token, err := store.AddDevice(devName, model, platform, ch, addr, now)
	if err != nil {
		return DeviceRecord{}, err
	}

	p.mu.Lock()
	if cur, exists := p.pending[pairID]; exists && cur.status == companionapi.PairStatusPending {
		cur.status = companionapi.PairStatusApproved
		cur.deviceID = dev.ID
		cur.token = token
		if !cur.closed {
			cur.closed = true
			close(cur.done)
		}
	}
	onDiff := p.onStateDiff
	p.mu.Unlock()
	if onDiff != nil {
		onDiff()
	}
	return dev, nil
}

// Reject denies a pending pairing request and notifies the waiting phone immediately.
func (p *pairingManager) Reject(pairID string) error {
	p.mu.Lock()
	rec, ok := p.pending[pairID]
	if !ok {
		p.mu.Unlock()
		return errors.New("配对请求不存在或已过期")
	}
	if rec.status == companionapi.PairStatusPending {
		rec.status = companionapi.PairStatusRejected
		if !rec.closed {
			rec.closed = true
			close(rec.done)
		}
	}
	onDiff := p.onStateDiff
	p.mu.Unlock()
	if onDiff != nil {
		onDiff()
	}
	return nil
}

// Poll waits up to maxWait for pairID to be decided, requiring exact SHA-256 match
// against the initiating pairSecret (mismatch returns found=false -> HTTP 404 per design §4.4).
// Once approved, the token is returned ONCE and pairID is deleted immediately.
func (p *pairingManager) Poll(ctx context.Context, pairID, pairSecret string, maxWait time.Duration) (companionapi.PairPollResponse, bool) {
	if strings.TrimSpace(pairID) == "" || strings.TrimSpace(pairSecret) == "" {
		return companionapi.PairPollResponse{}, false
	}
	gotHash := HashSecret(strings.TrimSpace(pairSecret))
	if maxWait <= 0 || maxWait > companionapi.PairPollMaxWait {
		maxWait = companionapi.PairPollMaxWait
	}

	p.mu.Lock()
	now := p.now()
	p.pruneExpiredLocked(now)
	rec, ok := p.pending[pairID]
	if !ok || subtle.ConstantTimeCompare([]byte(rec.pairSecretHash), []byte(gotHash)) != 1 {
		p.mu.Unlock()
		return companionapi.PairPollResponse{}, false
	}
	if rec.status != companionapi.PairStatusPending {
		resp := p.consumeResultLocked(rec)
		p.mu.Unlock()
		return resp, true
	}
	doneCh := rec.done
	remaining := rec.expiresAt.Sub(now)
	if remaining < maxWait {
		maxWait = remaining
	}
	p.mu.Unlock()

	if maxWait > 0 {
		timer := time.NewTimer(maxWait)
		defer timer.Stop()
		select {
		case <-doneCh:
		case <-timer.C:
		case <-ctx.Done():
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	now = p.now()
	p.pruneExpiredLocked(now)
	rec, ok = p.pending[pairID]
	if !ok || subtle.ConstantTimeCompare([]byte(rec.pairSecretHash), []byte(gotHash)) != 1 {
		return companionapi.PairPollResponse{}, false
	}
	if rec.status == companionapi.PairStatusPending {
		return companionapi.PairPollResponse{Status: companionapi.PairStatusPending}, true
	}
	return p.consumeResultLocked(rec), true
}

func (p *pairingManager) consumeResultLocked(rec *pendingPairRecord) companionapi.PairPollResponse {
	resp := companionapi.PairPollResponse{
		Status:   rec.status,
		DeviceID: rec.deviceID,
		Token:    rec.token,
	}
	// Delete pairId immediately once finalized so the token is returned at most once (design §4.4).
	rec.token = ""
	delete(p.pending, rec.pairID)
	return resp
}

func (p *pairingManager) pruneExpiredLocked(now time.Time) {
	for id, rec := range p.pending {
		if !now.Before(rec.expiresAt) {
			if rec.status == companionapi.PairStatusPending {
				rec.status = companionapi.PairStatusExpired
				if !rec.closed {
					rec.closed = true
					close(rec.done)
				}
			}
			// Keep expired marker briefly (30s) so an active poller sees "expired",
			// then delete it from memory.
			if now.Sub(rec.expiresAt) > 30*time.Second {
				rec.token = ""
				delete(p.pending, id)
			}
		}
	}
}

func (p *pairingManager) ClearAll() {
	p.mu.Lock()
	p.active = nil
	for id, rec := range p.pending {
		if rec.status == companionapi.PairStatusPending {
			rec.status = companionapi.PairStatusRejected
			if !rec.closed {
				rec.closed = true
				close(rec.done)
			}
		}
		rec.token = ""
		delete(p.pending, id)
	}
	onDiff := p.onStateDiff
	p.mu.Unlock()
	if onDiff != nil {
		onDiff()
	}
}

func generateShortCode() (string, error) {
	alphabet := companionapi.ShortCodeAlphabet
	max := big.NewInt(int64(len(alphabet)))
	var b strings.Builder
	b.Grow(companionapi.ShortCodeLength)
	for i := 0; i < companionapi.ShortCodeLength; i++ {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			return "", err
		}
		b.WriteByte(alphabet[n.Int64()])
	}
	return b.String(), nil
}
