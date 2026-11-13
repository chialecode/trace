package subscription

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// The JSON document is authoritative. Failed file/SQL writes must restore the
// previous SQLite view, including both sides of a member move.
func (m *Manager) restoreSubscriptionViewLocked(before []Subscription) error {
	current, err := m.listLocked()
	if err != nil {
		return err
	}
	oldJSON, _ := json.Marshal(before)
	newJSON, _ := json.Marshal(current)
	if bytes.Equal(oldJSON, newJSON) {
		return nil
	}
	tx, err := m.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	oldByID, currentByID := make(map[string]Subscription), make(map[string]Subscription)
	for _, sub := range before {
		oldByID[sub.SubID] = sub
	}
	for _, sub := range current {
		currentByID[sub.SubID] = sub
		if _, exists := oldByID[sub.SubID]; !exists {
			if _, err := tx.Exec(`DELETE FROM subscriptions WHERE sub_id = ?`, sub.SubID); err != nil {
				return err
			}
		}
	}
	for _, sub := range before {
		previous, _ := json.Marshal(sub)
		present, exists := currentByID[sub.SubID]
		presentJSON, _ := json.Marshal(present)
		if exists && bytes.Equal(previous, presentJSON) {
			continue
		}
		if err := upsertSubscriptionRow(tx, sub); err != nil {
			return err
		}
	}
	return tx.Commit()
}

type subscriptionExecutor interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func validateSubscriptionInput(name, note string, accounts []AccountRef) error {
	if utf8.RuneCountInString(strings.TrimSpace(name)) > 64 || utf8.RuneCountInString(note) > 500 {
		return fmt.Errorf("subscription: 名称上限 64 字，备注上限 500 字")
	}
	if len(accounts) > 20 {
		return fmt.Errorf("subscription: 一个订阅最多关联 20 个账号")
	}
	seen := make(map[string]bool)
	for _, account := range accounts {
		norm, err := NormalizeAndValidateAccount(account)
		if err != nil {
			return err
		}
		key := norm.Site + ":" + norm.UID
		if seen[key] {
			return fmt.Errorf("subscription: 同一账号不能重复关联")
		}
		seen[key] = true
	}
	return nil
}
