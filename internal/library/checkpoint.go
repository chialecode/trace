package library

import "fmt"

// SaveRecordingCheckpoint writes the fsynced fragment boundary. Finished media
// has already been hashed during finalization; only size/path checks are needed
// here, avoiding a reread of every prior recording segment once per fragment.
func (s *Store) SaveRecordingCheckpoint(itemDir string, meta ItemMetadata) (ItemMetadata, error) {
	if meta.Kind != "live" || meta.Status != "recording" || meta.Live == nil {
		return ItemMetadata{}, fmt.Errorf("library: 无效的录制断点")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveItemLocked(itemDir, meta, false)
}
