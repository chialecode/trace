package service

import (
	"path/filepath"

	"github.com/chialecode/trace/internal/update"
)

const devUninstallIdentifier = "io.github.chialecode.trace-dev"

func (b *Backend) initUpdateHistory(opts Options) error {
	path := filepath.Join(b.loc.DataDir, update.HistoryFileName)
	store, backup, err := update.OpenHistoryStore(path, b.redactStoredSecrets)
	if err != nil {
		return err
	}
	b.updateHistory = store
	if opts.InstalledDevVersion != nil {
		b.installedDevVersion = opts.InstalledDevVersion
	} else if b.loc.Mode == "installed" && !opts.DisableBgWorkers {
		b.installedDevVersion = ReadSystemInstalledDevVersion
	} else {
		b.installedDevVersion = func() string { return "" }
	}
	if backup != "" {
		b.logMu.Lock()
		b.corruptFiles = append(b.corruptFiles, backup)
		b.logMu.Unlock()
		b.logInfo("recovered corrupted update-history.json; backup kept at " + filepath.Base(backup))
	}
	return nil
}

// GetUpdateHistory returns a sanitized snapshot of <DataDir>/update-history.json.
func (b *Backend) GetUpdateHistory() update.History {
	if b == nil || b.updateHistory == nil {
		return update.History{
			Version: update.HistorySchemaVersion,
			Checks:  []update.CheckRecord{},
			Updates: []update.UpdateRecord{},
		}
	}
	return b.updateHistory.Get()
}

// RecordUpdateCheck appends one sanitized check record (retaining latest 20) to update-history.json.
func (b *Backend) RecordUpdateCheck(rec update.CheckRecord) error {
	if b == nil || b.updateHistory == nil {
		return nil
	}
	if err := b.updateHistory.RecordCheck(rec); err != nil {
		return err
	}
	if b.events != nil {
		b.events.Publish(EventSettingsChanged, "update-history")
	}
	return nil
}

// RecordAppUpdate appends one sanitized update record (retaining latest 50) to update-history.json.
func (b *Backend) RecordAppUpdate(rec update.UpdateRecord) error {
	if b == nil || b.updateHistory == nil {
		return nil
	}
	if err := b.updateHistory.RecordUpdate(rec); err != nil {
		return err
	}
	if b.events != nil {
		b.events.Publish(EventSettingsChanged, "update-history")
	}
	return nil
}

// InstalledDevVersion returns the DisplayVersion of the local TRACE Dev uninstall entry, or "".
func (b *Backend) InstalledDevVersion() string {
	if b == nil || b.installedDevVersion == nil {
		return ""
	}
	return update.SanitizeVersion(b.installedDevVersion())
}
