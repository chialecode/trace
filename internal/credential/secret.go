package credential

import (
	"errors"
	"os"
)

// KeyFileName is the key file of the credentials folder on Linux (unused
// on Windows, where DPAPI holds the key).
const KeyFileName = "secret.key"

// Secrets other than site Cookies (the remote's access token) are kept with
// the same protection as the site credentials: DPAPI on Windows, the key file
// elsewhere. The entropy keeps each kind apart.
const RemoteTokenEntropy = "trace:remote:token:v1"

// SaveSecret seals data into path (0600).
func SaveSecret(path, entropy string, data []byte) error {
	sealed, err := protectFor(path, data, []byte(entropy))
	if err != nil {
		return err
	}
	return writeFileAtomic(path, sealed, 0o600)
}

// LoadSecret opens a secret written by SaveSecret; ok is false when there is
// none or it cannot be opened any more.
func LoadSecret(path, entropy string) (data []byte, ok bool, err error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	plain, err := unprotectFor(path, raw, []byte(entropy))
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return nil, false, err
		}
		return nil, false, nil
	}
	return plain, true, nil
}

// DeleteSecret removes a secret; a missing one is not an error.
func DeleteSecret(path string) error {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}
