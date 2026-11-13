package credential

import (
	"path/filepath"
	"strings"
)

// Manager owns isolated per-site credential stores (bilibili.bin and douyin.bin)
// so expiry, logout, and encryption entropy remain strictly independent per site.
type Manager struct {
	dir    string
	stores map[string]*Store
}

func normalizeSiteID(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case SiteBilibili:
		return SiteBilibili
	case SiteDouyin:
		return SiteDouyin
	default:
		return ""
	}
}

// OpenManager opens bilibili.bin and douyin.bin inside credDir with isolated DPAPI entropies.
// Any corrupt or wrong-entropy files are backed up and reported in backups without failing startup.
func OpenManager(credDir string) (*Manager, []string, error) {
	biliStore, biliBackup, err := Open(filepath.Join(credDir, BilibiliFileName))
	if err != nil {
		return nil, nil, err
	}
	dyStore, dyBackup, err := OpenDouyin(filepath.Join(credDir, DouyinFileName))
	if err != nil {
		return nil, nil, err
	}
	var backups []string
	if biliBackup != "" {
		backups = append(backups, biliBackup)
	}
	if dyBackup != "" {
		backups = append(backups, dyBackup)
	}
	return &Manager{
		dir: credDir,
		stores: map[string]*Store{
			SiteBilibili: biliStore,
			SiteDouyin:   dyStore,
		},
	}, backups, nil
}

// NewManagerFromStores constructs a Manager from pre-opened per-site Stores.
func NewManagerFromStores(stores map[string]*Store) *Manager {
	m := &Manager{
		stores: make(map[string]*Store, len(stores)),
	}
	for k, st := range stores {
		if id := normalizeSiteID(k); id != "" && st != nil {
			m.stores[id] = st
		}
	}
	return m
}

// Store returns the credential Store for siteID ("bilibili" or "douyin"), or nil if unknown.
func (m *Manager) Store(siteID string) *Store {
	if m == nil {
		return nil
	}
	return m.stores[normalizeSiteID(siteID)]
}

// Bilibili returns the Bilibili credential Store.
func (m *Manager) Bilibili() *Store {
	return m.Store(SiteBilibili)
}

// Douyin returns the Douyin credential Store.
func (m *Manager) Douyin() *Store {
	return m.Store(SiteDouyin)
}

// Status returns the desensitized Status for siteID.
func (m *Manager) Status(siteID string) Status {
	st := m.Store(siteID)
	if st == nil {
		return Status{
			Site:     normalizeSiteID(siteID),
			LoggedIn: false,
			State:    "logged_out",
		}
	}
	return st.Status()
}

// Statuses returns desensitized account statuses keyed by site ID ("bilibili", "douyin").
func (m *Manager) Statuses() map[string]Status {
	return map[string]Status{
		SiteBilibili: m.Status(SiteBilibili),
		SiteDouyin:   m.Status(SiteDouyin),
	}
}

// HasActiveCredential reports whether siteID holds an active login credential.
func (m *Manager) HasActiveCredential(siteID string) bool {
	st := m.Store(siteID)
	return st != nil && st.HasActiveCredential()
}

// MarkState updates state and message on siteID without affecting other sites.
func (m *Manager) MarkState(siteID, state, message string) (Status, error) {
	st := m.Store(siteID)
	if st == nil {
		return Status{}, ErrUnknownSite
	}
	return st.MarkState(state, message)
}

// MarkExpired marks only siteID's credential as expired, preserving all other sites.
func (m *Manager) MarkExpired(siteID, message string) (Status, error) {
	return m.MarkState(siteID, "expired", message)
}

// Clear deletes only siteID's credential file and resets its state to logged_out.
func (m *Manager) Clear(siteID string) error {
	st := m.Store(siteID)
	if st == nil {
		return ErrUnknownSite
	}
	return st.Clear()
}

// RedactString scrubs secrets stored across ALL managed site stores from in.
func (m *Manager) RedactString(in string) string {
	if in == "" || m == nil {
		return in
	}
	var allSecrets []string
	for _, id := range []string{SiteBilibili, SiteDouyin} {
		if st := m.stores[id]; st != nil {
			allSecrets = append(allSecrets, st.collectSecrets()...)
		}
	}
	return redactWithSecrets(in, allSecrets)
}
