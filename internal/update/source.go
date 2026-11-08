// Package update is TRACE's side of the in-app updates of the release
// channel (FR-UPD-002, FR-UPD-003): the source the updater reads releases
// from, with the checks TRACE adds to MyGo's, and the controller that
// schedules checks and downloads and tells the interface what to show.
//
// The package does not depend on MyGo (INV-05): internal/shell adapts
// mygo.Updater to Engine and installs Source with mygo.Updater.SetSource,
// whose interface Source satisfies.
package update

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Build-time values, injected with -ldflags -X by tools/package and
// tools/release from the updates of the channel's build configuration:
// the manifest feed and the public key that updates are signed with. They
// are empty in builds without updates (the development channel).
var (
	feedURL   = ""
	publicKey = ""
)

// Feed returns the injected manifest feed and public key.
func Feed() (feed, key string) {
	return strings.TrimSpace(feedURL), strings.TrimSpace(publicKey)
}

// Limits of what TRACE downloads, the same as the updater's.
const (
	MaxManifestSize = 1 << 20
	MaxArchiveSize  = 1 << 30
)

// AttestationField is the manifest field that tools/release adds: the
// Ed25519 signature, with the update key, of AttestationMessage. MyGo signs
// the archive's digest but not its version, so without it a manifest could
// offer an older signed archive under a newer version number.
const AttestationField = "traceAttestation"

// AttestationMessage binds a version to the signature of its archive,
// which in turn binds the archive's SHA-256.
func AttestationMessage(version, archiveSignature string) []byte {
	return []byte("trace-update-v1\n" + strings.TrimSpace(version) + "\n" + strings.TrimSpace(archiveSignature))
}

// SignAttestation signs AttestationMessage with the update key.
func SignAttestation(key ed25519.PrivateKey, version, archiveSignature string) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(key, AttestationMessage(version, archiveSignature)))
}

// ErrRejected marks an update that TRACE refused: the current version
// stays installed.
var ErrRejected = errors.New("update rejected")

func rejectf(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrRejected, fmt.Sprintf(format, args...))
}

// Manifest is the update manifest that `mygo build` writes, with TRACE's
// attestation.
type Manifest struct {
	Version     string    `json:"version"`
	Notes       string    `json:"notes,omitempty"`
	Date        string    `json:"date,omitempty"`
	URL         string    `json:"url"`
	Size        int64     `json:"size"`
	Signature   string    `json:"signature"`
	Deltas      []Delta   `json:"deltas,omitempty"`
	Previous    []Archive `json:"previous,omitempty"`
	Attestation string    `json:"traceAttestation,omitempty"`
}

// Delta is a delta update of the manifest.
type Delta struct {
	From      string `json:"from"`
	URL       string `json:"url"`
	Size      int64  `json:"size"`
	Signature string `json:"signature"`
}

// Archive is an earlier archive the manifest lists for the next build.
type Archive struct {
	Version   string `json:"version"`
	URL       string `json:"url"`
	Size      int64  `json:"size"`
	Signature string `json:"signature"`
}

// ParsePublicKey decodes a base64 Ed25519 public key.
func ParsePublicKey(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("invalid update public key")
	}
	return ed25519.PublicKey(b), nil
}

// Source serves the manifest and the files it names to the updater
// (mygo.UpdateSource) with TRACE's checks:
//
//   - every request is HTTPS, redirects included (plain HTTP only to the
//     loopback interface, and only when AllowLoopbackHTTP is set, which
//     the ptest identity alone does);
//   - the manifest is complete, within the size limits, of a version
//     newer than the running one, and carries a valid attestation of that
//     version with the update key;
//   - every downloaded file has the size and the signature the manifest
//     gives, which is checked as it is read: a file that does not match
//     fails before the updater sees its end.
//
// A manifest of a version that is not newer is passed on as it is: the
// updater reports no update and the current version stays.
type Source struct {
	ManifestURL       string
	PublicKey         ed25519.PublicKey
	Current           string
	Client            *http.Client
	AllowLoopbackHTTP bool
	// Rewrite, when set, maps the URLs the manifest names to where the
	// files are served from (the ptest identity's local release).
	Rewrite func(string) string

	mu    sync.Mutex
	files map[string]expectedFile
}

type expectedFile struct {
	size      int64
	signature string
}

// Manifest fetches and checks the manifest of the platform.
func (s *Source) Manifest(ctx context.Context, prereleases bool) (io.ReadCloser, error) {
	raw, err := s.get(ctx, s.ManifestURL, MaxManifestSize)
	if err != nil {
		return nil, err
	}
	m, err := s.Verify(raw)
	if err != nil {
		return nil, err
	}
	files := map[string]expectedFile{}
	if m != nil {
		files[m.URL] = expectedFile{size: m.Size, signature: m.Signature}
		for _, d := range m.Deltas {
			files[d.URL] = expectedFile{size: d.Size, signature: d.Signature}
		}
	}
	s.mu.Lock()
	s.files = files
	s.mu.Unlock()
	return io.NopCloser(bytes.NewReader(raw)), nil
}

// Verify parses a manifest and checks it. It returns nil and no error for
// a manifest that is complete but not newer than the running version.
func (s *Source) Verify(raw []byte) (*Manifest, error) {
	if len(raw) > MaxManifestSize {
		return nil, rejectf("manifest larger than %d bytes", MaxManifestSize)
	}
	var m Manifest
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&m); err != nil {
		return nil, rejectf("manifest is not valid JSON: %v", err)
	}
	if m.Version == "" || m.URL == "" || m.Signature == "" || m.Size <= 0 {
		return nil, rejectf("incomplete manifest")
	}
	if !validVersion(m.Version) {
		return nil, rejectf("manifest version %q is not a version", m.Version)
	}
	if Compare(m.Version, s.Current) <= 0 {
		return nil, nil
	}
	if m.Size > MaxArchiveSize {
		return nil, rejectf("archive size %d is out of range", m.Size)
	}
	if err := s.checkURL(m.URL); err != nil {
		return nil, err
	}
	if err := checkSignatureFormat(m.Signature); err != nil {
		return nil, err
	}
	for _, d := range m.Deltas {
		if d.Size <= 0 || d.Size > MaxArchiveSize || d.From == "" {
			return nil, rejectf("delta from %q is incomplete", d.From)
		}
		if err := s.checkURL(d.URL); err != nil {
			return nil, err
		}
		if err := checkSignatureFormat(d.Signature); err != nil {
			return nil, err
		}
	}
	if len(s.PublicKey) != ed25519.PublicKeySize {
		return nil, rejectf("no update key")
	}
	att, err := base64.StdEncoding.DecodeString(strings.TrimSpace(m.Attestation))
	if err != nil || len(att) != ed25519.SignatureSize {
		return nil, rejectf("manifest of %s has no valid attestation", m.Version)
	}
	if !ed25519.Verify(s.PublicKey, AttestationMessage(m.Version, m.Signature), att) {
		return nil, rejectf("the attestation of %s is not signed with the app's key", m.Version)
	}
	return &m, nil
}

// Open downloads a file the manifest names and checks its size and
// signature while it is read.
func (s *Source) Open(ctx context.Context, rawURL string) (io.ReadCloser, error) {
	s.mu.Lock()
	want, ok := s.files[rawURL]
	s.mu.Unlock()
	if !ok {
		return nil, rejectf("%s is not a file of the checked manifest", path.Base(rawURL))
	}
	if err := s.checkURL(rawURL); err != nil {
		return nil, err
	}
	target := rawURL
	if s.Rewrite != nil {
		target = s.Rewrite(rawURL)
	}
	body, err := s.open(ctx, target)
	if err != nil {
		return nil, err
	}
	return &verifyingReader{body: body, limit: io.LimitReader(body, want.size+1), want: want, key: s.PublicKey, sum: sha256.New(), name: path.Base(rawURL)}, nil
}

func (s *Source) get(ctx context.Context, rawURL string, limit int64) ([]byte, error) {
	body, err := s.open(ctx, rawURL)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	raw, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) > limit {
		return nil, rejectf("manifest larger than %d bytes", limit)
	}
	return raw, nil
}

func (s *Source) open(ctx context.Context, rawURL string) (io.ReadCloser, error) {
	if err := s.checkTransportURL(rawURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	// Only the product and platform; GitHub's asset store rejects
	// non-ASCII user agents.
	req.Header.Set("User-Agent", "trace-updater")
	client := s.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Minute}
	}
	c := *client
	c.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return s.checkTransportURL(r.URL.String())
	}
	resp, err := c.Do(req)
	if err != nil {
		if errors.Is(err, ErrRejected) {
			return nil, err
		}
		var ue *url.Error
		if errors.As(err, &ue) && errors.Is(ue.Err, ErrRejected) {
			return nil, ue.Err
		}
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, &HTTPStatusError{Status: resp.StatusCode}
	}
	return resp.Body, nil
}

// HTTPStatusError is a response other than 200 from the update server.
type HTTPStatusError struct{ Status int }

func (e *HTTPStatusError) Error() string {
	return "update server answered HTTP " + strconv.Itoa(e.Status)
}

// checkURL checks a URL the manifest names.
func (s *Source) checkURL(raw string) error {
	if s.Rewrite != nil {
		// The ptest identity serves the files itself; the names still
		// have to be HTTPS URLs as a real release's are.
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host == "" {
			return rejectf("%q is not an HTTPS URL", raw)
		}
		return nil
	}
	return s.checkTransportURL(raw)
}

// checkTransportURL checks a URL that is requested.
func (s *Source) checkTransportURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return rejectf("%q is not a URL", raw)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if s.AllowLoopbackHTTP && isLoopback(u.Hostname()) {
			return nil
		}
	}
	return rejectf("refusing %s: updates need HTTPS", u.Redacted())
}

func isLoopback(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func checkSignatureFormat(sig string) error {
	b, err := base64.StdEncoding.DecodeString(sig)
	if err != nil || len(b) != ed25519.SignatureSize {
		return rejectf("signature is not an Ed25519 signature")
	}
	return nil
}

// verifyingReader checks size and signature as the file is read and
// reports a mismatch as the read error in place of the end of the file.
type verifyingReader struct {
	body  io.ReadCloser
	limit io.Reader
	want  expectedFile
	key   ed25519.PublicKey
	sum   hash.Hash
	n     int64
	name  string
	done  bool
}

func (r *verifyingReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	n, err := r.limit.Read(p)
	r.n += int64(n)
	r.sum.Write(p[:n])
	if r.n > r.want.size {
		return 0, rejectf("%s is larger than the manifest says", r.name)
	}
	if err == io.EOF {
		if r.n != r.want.size {
			return n, rejectf("%s has %d bytes, the manifest says %d", r.name, r.n, r.want.size)
		}
		sig, _ := base64.StdEncoding.DecodeString(r.want.signature)
		if !ed25519.Verify(r.key, r.sum.Sum(nil), sig) {
			return n, rejectf("%s is not signed with the app's key", r.name)
		}
		r.done = true
	}
	return n, err
}

func (r *verifyingReader) Close() error { return r.body.Close() }

// Compare compares versions as the updater does: numbers numerically, a
// prerelease before its release.
func Compare(a, b string) int {
	a, b = strings.TrimPrefix(a, "v"), strings.TrimPrefix(b, "v")
	a, _, _ = strings.Cut(a, "+")
	b, _, _ = strings.Cut(b, "+")
	ar, apre, _ := strings.Cut(a, "-")
	br, bpre, _ := strings.Cut(b, "-")
	if c := compareFields(ar, br); c != 0 {
		return c
	}
	switch {
	case apre == bpre:
		return 0
	case apre == "":
		return 1
	case bpre == "":
		return -1
	}
	return compareFields(apre, bpre)
}

func compareFields(a, b string) int {
	af, bf := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(af) || i < len(bf); i++ {
		var x, y string
		if i < len(af) {
			x = af[i]
		}
		if i < len(bf) {
			y = bf[i]
		}
		xn, xerr := strconv.Atoi(x)
		yn, yerr := strconv.Atoi(y)
		switch {
		case x == y:
			continue
		case x == "":
			return -1
		case y == "":
			return 1
		case xerr == nil && yerr == nil:
			if xn < yn {
				return -1
			}
			if xn > yn {
				return 1
			}
		case xerr == nil:
			return -1
		case yerr == nil:
			return 1
		default:
			return strings.Compare(x, y)
		}
	}
	return 0
}

func validVersion(v string) bool {
	core, _, _ := strings.Cut(strings.TrimPrefix(v, "v"), "-")
	core, _, _ = strings.Cut(core, "+")
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return false
	}
	for _, p := range parts {
		if _, err := strconv.Atoi(p); err != nil || p == "" {
			return false
		}
	}
	return true
}
