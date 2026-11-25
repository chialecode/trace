package library

import "fmt"

// SaveLiveItem persists a live recording item without re-hashing finalized
// segments whose SHA-256 was already computed during conversion; it still
// validates symlink/junction safety, regular-file mode, and exact byte sizes.
func (s *Store) SaveLiveItem(itemDir string, meta ItemMetadata) (ItemMetadata, error) {
	if meta.Kind != "live" {
		return ItemMetadata{}, fmt.Errorf("library: 无效的直播条目")
	}
	return s.saveItem(itemDir, meta, false)
}

// SaveRecordingCheckpoint writes the fsynced fragment boundary. Finished media
// has already been hashed during finalization; only size/path checks are needed
// here, avoiding a reread of every prior recording segment once per fragment.
func (s *Store) SaveRecordingCheckpoint(itemDir string, meta ItemMetadata) (ItemMetadata, error) {
	if meta.Kind != "live" || meta.Status != "recording" || meta.Live == nil {
		return ItemMetadata{}, fmt.Errorf("library: 无效的录制断点")
	}
	return s.saveItem(itemDir, meta, false)
}
