// Package remoteserver is trace-server's HTTPS API and its setup: the
// self-signed certificate, the access token (only its hash is stored),
// failure limiting and the routes that hand requests to the service layer
// (docs/design/remote.md §2, §3). It never links the desktop UI.
package remoteserver

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/remote"
	"github.com/chialecode/trace/internal/storage"
	"github.com/chialecode/trace/internal/upload"
)

// Files of the data directory.
const (
	ConfigFile = "server.json"
	TLSDir     = "tls"
	CertFile   = "cert.pem"
	KeyFile    = "key.pem"
)

// Config is server.json. It never holds the token itself.
type Config struct {
	Version    int           `json:"version"`
	Listen     string        `json:"listen"`
	PublicURL  string        `json:"publicUrl"`
	ExportDir  string        `json:"exportDir"`
	Node       string        `json:"node"`
	Name       string        `json:"name,omitempty"`
	TokenHash  string        `json:"tokenHash"`
	TokenSetAt string        `json:"tokenSetAt"`
	Upload     upload.Config `json:"upload"`
}

// LoadConfig reads server.json from dataDir.
func LoadConfig(dataDir string) (Config, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, ConfigFile))
	if err != nil {
		if os.IsNotExist(err) {
			return Config{}, fmt.Errorf("%s 里没有 %s：请先运行 trace-server init", dataDir, ConfigFile)
		}
		return Config{}, err
	}
	var c Config
	if err := json.Unmarshal(raw, &c); err != nil {
		return Config{}, fmt.Errorf("%s 格式不正确：%w", ConfigFile, err)
	}
	if c.Version != 1 {
		return Config{}, fmt.Errorf("%s 的版本 %d 不是这个程序的版本", ConfigFile, c.Version)
	}
	if strings.TrimSpace(c.Listen) == "" || c.ExportDir == "" || c.TokenHash == "" {
		return Config{}, fmt.Errorf("%s 不完整：请重新运行 trace-server init", ConfigFile)
	}
	if c.Upload, err = c.Upload.Normalize(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// SaveConfig writes server.json (0600).
func SaveConfig(dataDir string, c Config) error {
	return storage.WriteJSONAtomic(filepath.Join(dataDir, ConfigFile), c, 0o600)
}

// InitOptions are trace-server init's flags.
type InitOptions struct {
	DataDir       string
	ExportDir     string
	Listen        string // default ":47380"
	PublicAddress string // host or host:port written into the code
	Name          string
}

// InitResult is what init prints once.
type InitResult struct {
	Config Config
	Code   string
	Conn   remote.Connection
}

// Init prepares a data directory: folders (0700), node ID, a self-signed
// certificate, the credential key and an access token. It refuses a data
// directory that already has server.json.
func Init(opt InitOptions) (InitResult, error) {
	if strings.TrimSpace(opt.DataDir) == "" || strings.TrimSpace(opt.ExportDir) == "" {
		return InitResult{}, errors.New("需要 --data 与 --export")
	}
	dataDir, err := filepath.Abs(opt.DataDir)
	if err != nil {
		return InitResult{}, err
	}
	exportDir, err := filepath.Abs(opt.ExportDir)
	if err != nil {
		return InitResult{}, err
	}
	if _, err := os.Stat(filepath.Join(dataDir, ConfigFile)); err == nil {
		return InitResult{}, fmt.Errorf("%s 已经初始化过；要换令牌请用 trace-server token", dataDir)
	}
	listen := strings.TrimSpace(opt.Listen)
	if listen == "" {
		listen = ":" + strconv.Itoa(remote.DefaultPort)
	}
	_, port, err := net.SplitHostPort(listen)
	if err != nil {
		return InitResult{}, fmt.Errorf("--listen 不正确：%w", err)
	}
	portNum, err := strconv.Atoi(port)
	if err != nil || portNum < 1 || portNum > 65535 {
		return InitResult{}, fmt.Errorf("--listen 端口不正确：%q", port)
	}
	host := strings.TrimSpace(opt.PublicAddress)
	if host == "" {
		host = firstLANAddress()
	}
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(strings.Trim(host, "[]"), port)
	}
	publicURL, err := remote.NormalizeURL("https://" + host)
	if err != nil {
		return InitResult{}, err
	}
	for _, d := range []string{dataDir, exportDir, filepath.Join(dataDir, TLSDir), filepath.Join(dataDir, "credentials"), filepath.Join(dataDir, "logs")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return InitResult{}, err
		}
		_ = os.Chmod(d, 0o700)
	}
	name := strings.TrimSpace(opt.Name)
	if name == "" {
		name, _ = os.Hostname()
	}
	node, err := library.LoadOrCreateNode(dataDir, name)
	if err != nil {
		return InitResult{}, err
	}
	fp, err := generateCertificate(filepath.Join(dataDir, TLSDir), hostOnly(host))
	if err != nil {
		return InitResult{}, err
	}
	if err := credential.EnsureKeyFile(filepath.Join(dataDir, "credentials")); err != nil {
		return InitResult{}, err
	}
	token, hash, err := newToken()
	if err != nil {
		return InitResult{}, err
	}
	cfg := Config{Version: 1, Listen: listen, PublicURL: publicURL, ExportDir: exportDir, Node: node.Node, Name: name,
		TokenHash: hash, TokenSetAt: time.Now().UTC().Format(time.RFC3339), Upload: upload.DefaultConfig()}
	if err := SaveConfig(dataDir, cfg); err != nil {
		return InitResult{}, err
	}
	conn := remote.Connection{URL: publicURL, Token: token, Fingerprint: fp, Name: name}
	return InitResult{Config: cfg, Code: remote.EncodeCode(conn), Conn: conn}, nil
}

// RotateToken replaces the token; the old one stops working at once (the
// running server detects changes to server.json and reloads the token hash).
func RotateToken(dataDir string) (InitResult, error) {
	cfg, err := LoadConfig(dataDir)
	if err != nil {
		return InitResult{}, err
	}
	fp, err := CertificateFingerprint(dataDir)
	if err != nil {
		return InitResult{}, err
	}
	token, hash, err := newToken()
	if err != nil {
		return InitResult{}, err
	}
	cfg.TokenHash, cfg.TokenSetAt = hash, time.Now().UTC().Format(time.RFC3339)
	if err := SaveConfig(dataDir, cfg); err != nil {
		return InitResult{}, err
	}
	conn := remote.Connection{URL: cfg.PublicURL, Token: token, Fingerprint: fp, Name: cfg.Name}
	return InitResult{Config: cfg, Code: remote.EncodeCode(conn), Conn: conn}, nil
}

func newToken() (token, hash string, err error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", err
	}
	token = base64.RawURLEncoding.EncodeToString(raw[:])
	return token, HashToken(token), nil
}

// HashToken is the stored form of a token.
func HashToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func hostOnly(hostport string) string {
	h, _, err := net.SplitHostPort(hostport)
	if err != nil {
		return strings.Trim(hostport, "[]")
	}
	return h
}

// firstLANAddress is the first non-loopback IPv4 address, or 127.0.0.1.
func firstLANAddress() string {
	addrs, err := net.InterfaceAddrs()
	if err == nil {
		for _, a := range addrs {
			if ipn, ok := a.(*net.IPNet); ok && !ipn.IP.IsLoopback() && ipn.IP.To4() != nil && !ipn.IP.IsLinkLocalUnicast() {
				return ipn.IP.String()
			}
		}
	}
	return "127.0.0.1"
}

// generateCertificate writes a 10-year self-signed ECDSA P-256 certificate
// and its key (0600) and returns the fingerprint.
func generateCertificate(dir, host string) (string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	tpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "trace-server"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"trace-server"},
	}
	if ip := net.ParseIP(host); ip != nil {
		tpl.IPAddresses = []net.IP{ip}
	} else if host != "" {
		tpl.DNSNames = append(tpl.DNSNames, host)
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", err
	}
	if err := storage.WriteFileAtomic(filepath.Join(dir, KeyFile), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return "", err
	}
	if err := storage.WriteFileAtomic(filepath.Join(dir, CertFile), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		return "", err
	}
	return remote.Fingerprint(der), nil
}

// CertificateFingerprint reads the certificate of dataDir.
func CertificateFingerprint(dataDir string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, TLSDir, CertFile))
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return "", errors.New("证书文件格式不正确")
	}
	return remote.Fingerprint(block.Bytes), nil
}
