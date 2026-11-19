//go:build windows

package main

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/update"
)

// localRelease serves the files of a release on the loopback interface,
// as GitHub would: the ptest identity alone may be pointed at it.
type localRelease struct {
	base            string
	srv             *http.Server
	mu              sync.Mutex
	files           map[string][]byte
	requests        []string
	tamperedArchive string
}

func startLocalRelease() (*localRelease, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	l := &localRelease{base: "http://" + ln.Addr().String() + "/trace-ptest", files: map[string][]byte{}}
	l.srv = &http.Server{Handler: http.HandlerFunc(l.handle), ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = l.srv.Serve(ln) }()
	return l, nil
}

func (l *localRelease) handle(w http.ResponseWriter, req *http.Request) {
	name := path.Base(req.URL.Path)
	l.mu.Lock()
	l.requests = append(l.requests, req.Method+" "+name)
	data, ok := l.files[name]
	l.mu.Unlock()
	if !ok {
		http.NotFound(w, req)
		return
	}
	http.ServeContent(w, req, name, time.Time{}, bytes.NewReader(data))
}

func (l *localRelease) serveDir(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	files := map[string][]byte{}
	for _, e := range entries {
		if n := e.Name(); e.Type().IsRegular() && (n == manifestName || strings.HasSuffix(n, ".tar.gz")) {
			raw, err := os.ReadFile(filepath.Join(dir, n))
			if err != nil {
				return err
			}
			files[n] = raw
		}
	}
	l.set(files)
	return nil
}

// serveTampered makes the local release offer a newer version whose
// archive is the new one with one byte changed: its attestation is valid,
// its signature no longer matches the content.
func (l *localRelease) serveTampered(newDir, newVersion, tamperedVersion string, key ed25519.PrivateKey) error {
	raw, err := os.ReadFile(filepath.Join(newDir, manifestName))
	if err != nil {
		return err
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return err
	}
	archiveName := "trace-ptest-" + newVersion + "-windows-amd64.tar.gz"
	archive, err := os.ReadFile(filepath.Join(newDir, archiveName))
	if err != nil {
		return err
	}
	archive[len(archive)/2] ^= 0xff
	tamperedName := "trace-ptest-" + tamperedVersion + "-windows-amd64.tar.gz"
	sig, _ := m["signature"].(string)
	m["version"] = tamperedVersion
	m["notes"] = "- 被篡改的更新包"
	m["url"] = "https://rehearsal.invalid/trace-ptest/" + tamperedName
	m[update.AttestationField] = update.SignAttestation(key, tamperedVersion, sig)
	manifest, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	l.set(map[string][]byte{manifestName: manifest, tamperedName: archive})
	l.tamperedArchive = tamperedName
	return nil
}

func (l *localRelease) set(files map[string][]byte) {
	l.mu.Lock()
	l.files = files
	l.mu.Unlock()
}

func (l *localRelease) served(name string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, r := range l.requests {
		if strings.HasSuffix(r, " "+name) {
			return true
		}
	}
	return false
}

func (l *localRelease) log() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.requests...)
}

func (l *localRelease) close() { _ = l.srv.Close() }
