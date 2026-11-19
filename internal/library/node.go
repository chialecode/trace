package library

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/storage"
)

// NodeFileName is the file in the data directory that holds this
// installation's node ID (docs/design/remote.md §6.1, §8).
const NodeFileName = "node.json"

type nodeFile struct {
	Version   int    `json:"version"`
	Node      string `json:"node"`
	Name      string `json:"name,omitempty"`
	CreatedAt string `json:"createdAt"`
}

// LoadOrCreateNode returns the node of dataDir, creating node.json with a
// random ID the first time. The ID never changes afterwards; name is used
// when the file has none (a name given later wins over the computer's name).
func LoadOrCreateNode(dataDir, name string) (ItemNode, error) {
	path := filepath.Join(dataDir, NodeFileName)
	raw, err := os.ReadFile(path)
	if err == nil {
		var f nodeFile
		if jErr := json.Unmarshal(raw, &f); jErr == nil && validNodeID(f.Node) {
			changed := false
			if strings.TrimSpace(name) != "" && f.Name != strings.TrimSpace(name) {
				f.Name = strings.TrimSpace(name)
				changed = true
			}
			if changed {
				_ = storage.WriteJSONAtomic(path, f, 0o600)
			}
			return ItemNode{Node: f.Node, Name: f.Name}, nil
		}
		if _, bErr := storage.BackupCorruptFile(path); bErr != nil {
			return ItemNode{}, bErr
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return ItemNode{}, err
	}
	var rnd [16]byte
	if _, err := rand.Read(rnd[:]); err != nil {
		return ItemNode{}, fmt.Errorf("library: node id: %w", err)
	}
	f := nodeFile{Version: 1, Node: hex.EncodeToString(rnd[:]), Name: strings.TrimSpace(name), CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := storage.WriteJSONAtomic(path, f, 0o600); err != nil {
		return ItemNode{}, err
	}
	return ItemNode{Node: f.Node, Name: f.Name}, nil
}

func validNodeID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

// SetNode makes every item saved from now on without a CachedBy record
// this installation as the one that cached it.
func (s *Store) SetNode(node ItemNode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if node.Node == "" {
		s.node = nil
		return
	}
	n := node
	s.node = &n
}

// Node is the installation set with SetNode, or the zero value.
func (s *Store) Node() ItemNode {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.node == nil {
		return ItemNode{}
	}
	return *s.node
}
