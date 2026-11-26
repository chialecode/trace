package companion

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/chialecode/trace/internal/companionapi"
	"github.com/chialecode/trace/internal/storage"
)

// TLSIdentity holds the loaded TLS 1.3 server configuration and its pinned fingerprint.
type TLSIdentity struct {
	TLSConfig    *tls.Config
	Fingerprint  string
	IdentityCode string
}

// EnsureTLSIdentity loads the companion ECDSA P-256 certificate from
// <dataDir>/companion/tls/ or generates a new 10-year self-signed certificate
// on first enable (docs/design/mobile.md §3).
func EnsureTLSIdentity(dataDir string) (TLSIdentity, error) {
	dir := filepath.Join(dataDir, filepath.FromSlash(TLSRelDir))
	certPath := filepath.Join(dir, CertFileName)
	keyPath := filepath.Join(dir, KeyFileName)

	if id, err := loadTLSIdentity(certPath, keyPath); err == nil {
		return id, nil
	}
	return RegenerateTLSIdentity(dataDir)
}

// RegenerateTLSIdentity generates a brand-new ECDSA P-256 self-signed certificate
// and overwrites <dataDir>/companion/tls/{cert.pem,key.pem} with 0600 permissions.
func RegenerateTLSIdentity(dataDir string) (TLSIdentity, error) {
	dir := filepath.Join(dataDir, filepath.FromSlash(TLSRelDir))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return TLSIdentity{}, fmt.Errorf("companion: mkdir tls: %w", err)
	}
	_ = os.Chmod(dir, 0o700)

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return TLSIdentity{}, fmt.Errorf("companion: generate ecdsa key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return TLSIdentity{}, fmt.Errorf("companion: generate serial: %w", err)
	}
	now := time.Now().UTC()
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "trace-companion"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"trace-companion", "localhost"},
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return TLSIdentity{}, fmt.Errorf("companion: create cert: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return TLSIdentity{}, fmt.Errorf("companion: marshal key: %w", err)
	}

	certPath := filepath.Join(dir, CertFileName)
	keyPath := filepath.Join(dir, KeyFileName)
	if err := storage.WriteFileAtomic(keyPath, pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return TLSIdentity{}, fmt.Errorf("companion: write key: %w", err)
	}
	if err := storage.WriteFileAtomic(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		return TLSIdentity{}, fmt.Errorf("companion: write cert: %w", err)
	}
	return loadTLSIdentity(certPath, keyPath)
}

func loadTLSIdentity(certPath, keyPath string) (TLSIdentity, error) {
	rawCert, err := os.ReadFile(certPath)
	if err != nil {
		return TLSIdentity{}, err
	}
	block, _ := pem.Decode(rawCert)
	if block == nil || block.Type != "CERTIFICATE" {
		return TLSIdentity{}, errors.New("companion: invalid cert pem")
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return TLSIdentity{}, err
	}
	fp := companionapi.Fingerprint(block.Bytes)
	idCode, err := companionapi.IdentityCode(fp)
	if err != nil {
		return TLSIdentity{}, err
	}
	return TLSIdentity{
		TLSConfig: &tls.Config{
			MinVersion:   tls.VersionTLS13,
			Certificates: []tls.Certificate{cert},
		},
		Fingerprint:  fp,
		IdentityCode: idCode,
	}, nil
}
