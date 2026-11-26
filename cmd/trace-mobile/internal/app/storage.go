package app

import (
	"errors"
	"sync"

	"github.com/chialecode/trace/internal/companionclient"
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/preferences"
)

const (
	prefPairingKey = "companion.pairing.v1"
	secretTokenKey = "companion.device_token.v1"
)

// Store abstracts phone-side persistence of pairing metadata (preferences) and
// the device token (mygo.Secrets). No content, lists, or thumbnails are stored.
type Store interface {
	LoadPairing() (companionclient.PairingRecord, bool)
	SavePairing(rec companionclient.PairingRecord) error
	LoadToken() (string, error)
	SaveToken(token string) error
	ClearAll() error
}

// MyGoStore persists pairing metadata in mygo preferences and the device token
// in mygo.Secrets (Android Keystore-encrypted).
type MyGoStore struct{}

func (MyGoStore) LoadPairing() (companionclient.PairingRecord, bool) {
	var rec companionclient.PairingRecord
	if !preferences.Get(prefPairingKey, &rec) {
		return companionclient.PairingRecord{}, false
	}
	if rec.Node == "" || rec.FP == "" || len(rec.Addrs) == 0 {
		return companionclient.PairingRecord{}, false
	}
	return rec, true
}

func (MyGoStore) SavePairing(rec companionclient.PairingRecord) error {
	return preferences.Set(prefPairingKey, rec)
}

func (MyGoStore) LoadToken() (string, error) {
	b, err := mygo.Secrets.Get(secretTokenKey)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func (MyGoStore) SaveToken(token string) error {
	if token == "" {
		return errors.New("empty device token")
	}
	return mygo.Secrets.Set(secretTokenKey, []byte(token))
}

func (MyGoStore) ClearAll() error {
	errPref := preferences.Delete(prefPairingKey)
	errSec := mygo.Secrets.Delete(secretTokenKey)
	if errors.Is(errSec, mygo.ErrSecretNotFound) {
		errSec = nil
	}
	if errPref != nil {
		return errPref
	}
	return errSec
}

// MemoryStore is an in-memory Store used by unit tests and ui.Tester.
type MemoryStore struct {
	mu      sync.Mutex
	pairing *companionclient.PairingRecord
	token   string
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{} }

func (s *MemoryStore) LoadPairing() (companionclient.PairingRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pairing == nil {
		return companionclient.PairingRecord{}, false
	}
	cp := *s.pairing
	cp.Addrs = append([]string(nil), s.pairing.Addrs...)
	return cp, true
}

func (s *MemoryStore) SavePairing(rec companionclient.PairingRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := rec
	cp.Addrs = append([]string(nil), rec.Addrs...)
	s.pairing = &cp
	return nil
}

func (s *MemoryStore) LoadToken() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == "" {
		return "", mygo.ErrSecretNotFound
	}
	return s.token, nil
}

func (s *MemoryStore) SaveToken(token string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.token = token
	return nil
}

func (s *MemoryStore) ClearAll() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pairing = nil
	s.token = ""
	return nil
}
