//go:build !windows

package credential

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// On Linux the credential file is sealed under a random key file
// (0600); the file holds no plain sentinel; a lost key file means "no
// Cookie" without a crash, and a new login works again.
func TestLinuxCredentialKeyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bilibili.bin")
	store, _, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	const sess = "SENTINEL_LINUX_SESS_5d2e"
	if _, err := store.SaveManual("SESSDATA="+sess+"; bili_jct=SENTINEL_LINUX_CSRF", "SENTINEL_LINUX_RT"); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(filepath.Join(dir, KeyFileName))
	if err != nil || st.Mode().Perm() != 0o600 || st.Size() != 32 {
		t.Fatalf("key file = %v %v", st, err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range []string{sess, "SENTINEL_LINUX_CSRF", "SENTINEL_LINUX_RT"} {
		if bytes.Contains(raw, []byte(s)) {
			t.Fatalf("the credential file holds %s in plain text", s)
		}
	}
	again, _, err := Open(path)
	if err != nil || !again.Status().LoggedIn || !bytes.Contains([]byte(again.CookieHeader()), []byte(sess)) {
		t.Fatalf("reopen = %+v %v", again.Status(), err)
	}

	// The same key with another site's entropy does not open it.
	if _, err := unprotectFor(path, raw, []byte("other-site")); err == nil {
		t.Fatal("sealed data opened with another site's entropy")
	}

	if err := os.Remove(filepath.Join(dir, KeyFileName)); err != nil {
		t.Fatal(err)
	}
	lost, _, err := Open(path)
	if err != nil {
		t.Fatalf("Open after the key file was lost: %v", err)
	}
	if lost.Status().LoggedIn || lost.CookieHeader() != "" {
		t.Fatalf("a credential was read without its key: %+v", lost.Status())
	}
	if _, err := lost.SaveManual("SESSDATA=fresh; bili_jct=x", ""); err != nil {
		t.Fatalf("a new login after the key was lost: %v", err)
	}
	if st, err := os.Stat(filepath.Join(dir, KeyFileName)); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("new key file = %v %v", st, err)
	}

	// The remote's token is sealed the same way.
	tok := filepath.Join(dir, "remote-x.bin")
	if err := SaveSecret(tok, RemoteTokenEntropy, []byte("SENTINEL_TOKEN_77")); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(tok); bytes.Contains(b, []byte("SENTINEL_TOKEN_77")) {
		t.Fatal("the token file holds the token in plain text")
	}
	if got, ok, err := LoadSecret(tok, RemoteTokenEntropy); err != nil || !ok || string(got) != "SENTINEL_TOKEN_77" {
		t.Fatalf("LoadSecret = %q %v %v", got, ok, err)
	}

	// Symlink keyfile must be rejected.
	symDir := t.TempDir()
	target := filepath.Join(symDir, "other.key")
	if err := os.WriteFile(target, bytes.Repeat([]byte("a"), 32), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(symDir, KeyFileName)); err != nil {
		t.Fatal(err)
	}
	if err := EnsureKeyFile(symDir); err == nil {
		t.Fatal("EnsureKeyFile accepted symlink key file")
	}
}
