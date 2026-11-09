package update

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// fakeRelease is a synthetic release served over HTTPS from 127.0.0.1.
type fakeRelease struct {
	srv      *httptest.Server
	key      ed25519.PrivateKey
	pub      ed25519.PublicKey
	files    map[string][]byte
	manifest []byte
}

func newKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return pub, priv
}

func signDigest(key ed25519.PrivateKey, data []byte) string {
	sum := sha256.Sum256(data)
	return base64.StdEncoding.EncodeToString(ed25519.Sign(key, sum[:]))
}

func newFakeRelease(t *testing.T) *fakeRelease {
	t.Helper()
	pub, priv := newKey(t)
	r := &fakeRelease{key: priv, pub: pub, files: map[string][]byte{}}
	r.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		name := strings.TrimPrefix(req.URL.Path, "/")
		if name == "update-windows-amd64.json" {
			_, _ = w.Write(r.manifest)
			return
		}
		data, ok := r.files[name]
		if !ok {
			http.NotFound(w, req)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

// publish serves version with archive, signed (or not) as the arguments say.
func (r *fakeRelease) publish(t *testing.T, version string, archive []byte, mutate func(*Manifest)) {
	t.Helper()
	name := "trace-" + version + "-windows-amd64.tar.gz"
	r.files[name] = archive
	m := Manifest{Version: version, URL: r.srv.URL + "/" + name, Size: int64(len(archive)), Signature: signDigest(r.key, archive), Notes: "## " + version}
	m.Attestation = SignAttestation(r.key, m.Version, m.Signature)
	if mutate != nil {
		mutate(&m)
	}
	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	r.manifest = raw
}

func (r *fakeRelease) source(current string) *Source {
	return &Source{ManifestURL: r.srv.URL + "/update-windows-amd64.json", PublicKey: r.pub, Current: current, Client: r.srv.Client()}
}

// fetch runs a check and downloads the archive as the updater does.
func fetch(t *testing.T, s *Source) (*Manifest, []byte, error) {
	t.Helper()
	ctx := context.Background()
	body, err := s.Manifest(ctx, false)
	if err != nil {
		return nil, nil, err
	}
	raw, _ := io.ReadAll(body)
	body.Close()
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, nil, err
	}
	if Compare(m.Version, s.Current) <= 0 {
		return nil, nil, nil
	}
	f, err := s.Open(ctx, m.URL)
	if err != nil {
		return &m, nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	return &m, data, err
}

func TestUpdateSourceAcceptsSignedNewerRelease(t *testing.T) {
	r := newFakeRelease(t)
	r.publish(t, "0.1.2", []byte("archive of 0.1.2"), nil)
	m, data, err := fetch(t, r.source("0.1.1"))
	if err != nil {
		t.Fatalf("signed release rejected: %v", err)
	}
	if m.Version != "0.1.2" || string(data) != "archive of 0.1.2" {
		t.Fatalf("got %+v %q", m, data)
	}
}

func TestUpdateSourceRejections(t *testing.T) {
	_, otherKey := newKey(t)
	cases := []struct {
		name    string
		archive string
		mutate  func(r *fakeRelease, m *Manifest)
		serve   func(r *fakeRelease) // changes what is served after the manifest was made
	}{
		{name: "signature from another key", mutate: func(r *fakeRelease, m *Manifest) {
			m.Signature = signDigest(otherKey, r.files["trace-0.1.2-windows-amd64.tar.gz"])
			m.Attestation = SignAttestation(r.key, m.Version, m.Signature)
		}},
		{name: "attestation from another key", mutate: func(r *fakeRelease, m *Manifest) {
			m.Attestation = SignAttestation(otherKey, m.Version, m.Signature)
		}},
		{name: "missing attestation", mutate: func(r *fakeRelease, m *Manifest) { m.Attestation = "" }},
		{name: "older archive relabeled as newer version", mutate: func(r *fakeRelease, m *Manifest) {
			// A signed archive of an earlier release offered as 0.1.2: its
			// attestation names its own version.
			m.Attestation = SignAttestation(r.key, "0.1.0", m.Signature)
		}},
		{name: "tampered content", serve: func(r *fakeRelease) {
			r.files["trace-0.1.2-windows-amd64.tar.gz"] = []byte("archive of 0.1.X")
		}},
		{name: "truncated content", serve: func(r *fakeRelease) {
			r.files["trace-0.1.2-windows-amd64.tar.gz"] = []byte("archive")
		}},
		{name: "longer content", serve: func(r *fakeRelease) {
			r.files["trace-0.1.2-windows-amd64.tar.gz"] = []byte("archive of 0.1.2 and more")
		}},
		{name: "plain HTTP archive URL", mutate: func(r *fakeRelease, m *Manifest) {
			m.URL = "http://example.com/trace-0.1.2-windows-amd64.tar.gz"
			m.Attestation = SignAttestation(r.key, m.Version, m.Signature)
		}},
		{name: "plain HTTP delta URL", mutate: func(r *fakeRelease, m *Manifest) {
			m.Deltas = []Delta{{From: "0.1.1", URL: "http://example.com/x.delta", Size: 10, Signature: m.Signature}}
		}},
		{name: "incomplete manifest", mutate: func(r *fakeRelease, m *Manifest) { m.Signature = "" }},
		{name: "oversized archive", mutate: func(r *fakeRelease, m *Manifest) { m.Size = MaxArchiveSize + 1 }},
		{name: "garbage signature", mutate: func(r *fakeRelease, m *Manifest) { m.Signature = "bm90IGEgc2lnbmF0dXJl" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newFakeRelease(t)
			r.publish(t, "0.1.2", []byte("archive of 0.1.2"), func(m *Manifest) {
				if tc.mutate != nil {
					tc.mutate(r, m)
				}
			})
			if tc.serve != nil {
				tc.serve(r)
			}
			_, _, err := fetch(t, r.source("0.1.1"))
			if err == nil || !errors.Is(err, ErrRejected) {
				t.Fatalf("expected a rejection, got %v", err)
			}
			if Describe(err) != MsgRejected {
				t.Fatalf("message %q", Describe(err))
			}
		})
	}
}

func TestUpdateSourceCorruptManifestRejected(t *testing.T) {
	r := newFakeRelease(t)
	r.manifest = []byte("{not json")
	_, err := r.source("0.1.1").Manifest(context.Background(), false)
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("corrupt manifest: %v", err)
	}
	r.manifest = []byte(strings.Repeat(" ", MaxManifestSize+10))
	_, err = r.source("0.1.1").Manifest(context.Background(), false)
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("oversized manifest: %v", err)
	}
	r.manifest = []byte(`{"version":"latest","url":"https://x/y","size":1,"signature":"x"}`)
	_, err = r.source("0.1.1").Manifest(context.Background(), false)
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("manifest with a non-version: %v", err)
	}
}

func TestUpdateSourceNotNewerIsNoUpdate(t *testing.T) {
	for _, v := range []string{"0.1.1", "0.1.0", "0.1.1-rc.1"} {
		r := newFakeRelease(t)
		r.publish(t, v, []byte("archive"), func(m *Manifest) { m.Attestation = "" })
		m, data, err := fetch(t, r.source("0.1.1"))
		if err != nil || m != nil || data != nil {
			t.Fatalf("%s: a version that is not newer must offer nothing: %+v %v", v, m, err)
		}
	}
}

func TestUpdateSourceRefusesPlainHTTPFeed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("plain HTTP feed was requested: %s", r.URL)
	}))
	defer srv.Close()
	pub, _ := newKey(t)
	s := &Source{ManifestURL: srv.URL + "/update-windows-amd64.json", PublicKey: pub, Current: "0.1.1"}
	_, err := s.Manifest(context.Background(), false)
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("plain HTTP on loopback without the ptest allowance: %v", err)
	}
	s.ManifestURL = "http://example.com/update.json"
	s.AllowLoopbackHTTP = true
	if _, err := s.Manifest(context.Background(), false); !errors.Is(err, ErrRejected) {
		t.Fatalf("plain HTTP to another host is refused even for ptest: %v", err)
	}
}

func TestUpdateSourceRefusesRedirectToHTTP(t *testing.T) {
	r := newFakeRelease(t)
	r.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		http.Redirect(w, req, "http://example.com/update.json", http.StatusFound)
	})
	_, err := r.source("0.1.1").Manifest(context.Background(), false)
	if !errors.Is(err, ErrRejected) {
		t.Fatalf("redirect to HTTP: %v", err)
	}
}

func TestUpdateSourceOpensOnlyManifestFiles(t *testing.T) {
	r := newFakeRelease(t)
	r.publish(t, "0.1.2", []byte("archive of 0.1.2"), nil)
	s := r.source("0.1.1")
	if _, err := s.Manifest(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Open(context.Background(), r.srv.URL+"/other.tar.gz"); !errors.Is(err, ErrRejected) {
		t.Fatalf("file outside the manifest: %v", err)
	}
}

func TestVersionCompare(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{
		{"0.1.2", "0.1.1", 1}, {"0.1.10", "0.1.9", 1}, {"0.1.1", "0.1.1", 0},
		{"0.1.1-dev.202611091530", "0.1.1", -1}, {"0.1.1-rehearsal.2", "0.1.1-rehearsal.1", 1},
		{"v0.2.0", "0.1.99", 1},
	} {
		if got := Compare(c.a, c.b); got != c.want {
			t.Errorf("Compare(%s, %s) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}
