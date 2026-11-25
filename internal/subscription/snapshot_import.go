package subscription

import (
	"crypto/rand"
	"encoding/hex"
	"slices"
	"strings"
	"time"
)

// ImportPreview is what 导入订阅 would change, shown before anything is
// written (docs/design/remote.md §6.4).
type ImportPreview struct {
	// New lists the imported subscriptions that become new subscriptions
	// (with the accounts that are not subscribed yet).
	New []ImportSub `json:"new"`
	// Existing lists imported accounts that already belong to a local
	// subscription: skipped by default, or merged into that subscription.
	Existing []ImportExistingAccount `json:"existing"`
	// Unchanged counts imported subscriptions whose every account is
	// already subscribed.
	Unchanged int `json:"unchanged"`
}

// ImportSub is an imported subscription that would be created.
type ImportSub struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Accounts    []string `json:"accounts"`
	Tags        []string `json:"tags"`
	TagsSummary string   `json:"tagsSummary"`
}

// ImportExistingAccount is an imported account a local subscription
// already has.
type ImportExistingAccount struct {
	Site          string `json:"site"`
	UID           string `json:"uid"`
	Name          string `json:"name"`
	ImportedSubID string `json:"importedSubId"`
	ImportedSub   string `json:"importedSub"`
	LocalSubID    string `json:"localSubId"`
	LocalSub      string `json:"localSub"`
}

// ImportChoices are the user's answers to a preview.
type ImportChoices struct {
	// MergeExisting adds an imported subscription's new accounts to the
	// local subscription that already has one of its accounts, instead of
	// creating a new subscription.
	MergeExisting bool `json:"mergeExisting"`
	// MergeTags also adds the imported subscription's capability tags to the
	// existing local subscription when MergeExisting is true (false keeps
	// the local subscription's existing tags).
	MergeTags bool `json:"mergeTags,omitempty"`
}

type importPlan struct {
	preview ImportPreview
	// per imported subscription: the accounts not subscribed yet, and the
	// local subscription owning one of its accounts ("" when none).
	fresh map[string][]SnapshotAccount
	owner map[string]string
}

func (m *Manager) planImportLocked(snap Snapshot) (importPlan, error) {
	plan := importPlan{fresh: map[string][]SnapshotAccount{}, owner: map[string]string{}}
	for _, x := range snap.Subscriptions {
		var fresh []SnapshotAccount
		for _, a := range x.Accounts {
			if owner, ok := m.ownerOfLocked(a.Site, a.UID); ok {
				plan.preview.Existing = append(plan.preview.Existing, ImportExistingAccount{
					Site:          a.Site,
					UID:           a.UID,
					Name:          a.Name,
					ImportedSubID: x.ID,
					ImportedSub:   x.Name,
					LocalSubID:    owner.ID,
					LocalSub:      owner.Name,
				})
				if plan.owner[x.ID] == "" {
					plan.owner[x.ID] = owner.ID
				}
				continue
			}
			fresh = append(fresh, a)
		}
		plan.fresh[x.ID] = fresh
		if len(fresh) == 0 {
			plan.preview.Unchanged++
			continue
		}
		names := make([]string, 0, len(fresh))
		for _, a := range fresh {
			names = append(names, firstNonEmptyStr(a.Name, a.UID))
		}
		tags := NormalizeTags(x.Tags)
		plan.preview.New = append(plan.preview.New, ImportSub{
			ID:          x.ID,
			Name:        x.Name,
			Accounts:    names,
			Tags:        tags,
			TagsSummary: TagsSummary(tags),
		})
	}
	return plan, nil
}

// PreviewImport reports what importing snap would do; it writes nothing.
func (m *Manager) PreviewImport(snap Snapshot) (ImportPreview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	plan, err := m.planImportLocked(snap)
	return plan.preview, err
}

// ApplyImport imports snap with the user's choices in one write. Accounts
// already subscribed keep their subscription and their check state; nothing
// is written when anything fails.
func (m *Manager) ApplyImport(snap Snapshot, choices ImportChoices) (ImportPreview, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	plan, err := m.planImportLocked(snap)
	if err != nil {
		return ImportPreview{}, err
	}
	current, err := m.listSubsOnlyLocked()
	if err != nil {
		return ImportPreview{}, err
	}
	subIndex := map[string]int{}
	for i, s := range current {
		subIndex[s.ID] = i
	}
	for _, x := range snap.Subscriptions {
		fresh := plan.fresh[x.ID]
		if len(fresh) == 0 {
			if owner := plan.owner[x.ID]; owner != "" && choices.MergeExisting {
				if i, ok := subIndex[owner]; ok {
					if len(x.Links) > 0 {
						current[i].Links = MergeCustomLinks(current[i].Links, x.Links)
					}
					if choices.MergeTags && len(x.Tags) > 0 {
						mergedTags := append(slices.Clone(current[i].Tags), x.Tags...)
						m.applyTagsLocked(&current[i], mergedTags)
					}
				}
			}
			continue
		}
		if owner := plan.owner[x.ID]; owner != "" && choices.MergeExisting {
			if i, ok := subIndex[owner]; ok {
				if len(x.Links) > 0 {
					current[i].Links = MergeCustomLinks(current[i].Links, x.Links)
				}
				if choices.MergeTags && len(x.Tags) > 0 {
					mergedTags := append(slices.Clone(current[i].Tags), x.Tags...)
					m.applyTagsLocked(&current[i], mergedTags)
				}
				var overflow []SnapshotAccount
				for _, a := range fresh {
					if len(current[i].Accounts) >= 20 {
						overflow = append(overflow, a)
						continue
					}
					current[i].Accounts = append(current[i].Accounts, AccountRef{
						Site:     a.Site,
						UID:      a.UID,
						UniqueID: a.UniqueID,
						WebRID:   a.WebRID,
						Name:     a.Name,
						Face:     a.Face,
						AddedAt:  m.nowFunc().UTC().Format(time.RFC3339),
					})
				}
				if len(overflow) == 0 {
					continue
				}
				// Do not silently drop accounts beyond the 20-account cap:
				// place overflow accounts into a new subscription instead.
				fresh = overflow
			}
		}
		if len(fresh) == 0 {
			continue
		}
		for len(fresh) > 0 {
			chunk := fresh
			if len(chunk) > 20 {
				chunk = fresh[:20]
			}
			fresh = fresh[len(chunk):]
			y := x
			y.Accounts = chunk
			if _, taken := subIndex[y.ID]; taken || strings.TrimSpace(y.ID) == "" {
				y.ID = newImportedSubID()
			}
			sub := m.snapshotToSubscriptionLocked(y)
			subIndex[sub.ID] = len(current)
			current = append(current, sub)
		}
	}
	if err := m.replaceAllLocked(current); err != nil {
		return ImportPreview{}, err
	}
	return plan.preview, nil
}

func newImportedSubID() string {
	var rnd [16]byte
	_, _ = rand.Read(rnd[:])
	return hex.EncodeToString(rnd[:])
}
