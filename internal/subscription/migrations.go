package subscription

// migrateBaselineSnapshots adds an idempotent, reversible classification for
// legacy first-check snapshots. The original rows, snapshots, and obsolete
// viewed markers remain untouched. Missing/invalid timestamps stay visible.
func (m *Manager) migrateBaselineSnapshots() error {
	tx, err := m.updatesDB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`PRAGMA table_info(update_item)`)
	if err != nil {
		return err
	}
	hasFlag := false
	for rows.Next() {
		var cid, notNull, primaryKey int
		var name, typ string
		var defaultValue any
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &primaryKey); err != nil {
			rows.Close()
			return err
		}
		hasFlag = hasFlag || name == "baseline_snapshot"
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !hasFlag {
		if _, err := tx.Exec(`ALTER TABLE update_item ADD COLUMN baseline_snapshot INTEGER NOT NULL DEFAULT 0`); err != nil {
			return err
		}
		if _, err := tx.Exec(`UPDATE update_item SET baseline_snapshot = 1
			WHERE julianday(discovered_at) <= (
				SELECT julianday(baseline_at) FROM account_state
				WHERE account_state.site = update_item.site AND account_state.uid = update_item.uid
			)`); err != nil {
			return err
		}
	}
	return tx.Commit()
}
