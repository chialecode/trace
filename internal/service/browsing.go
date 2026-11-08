package service

import (
	"context"

	"github.com/chialecode/trace/internal/library"
	"github.com/chialecode/trace/internal/live"
	"github.com/chialecode/trace/internal/site/bilibili"
	"github.com/chialecode/trace/internal/subscription"
)

func (b *Backend) requireLogin() error { return b.bili.RequireAuth("service") }

func (b *Backend) LibraryStatistics() (library.Statistics, error) {
	if err := b.requireLogin(); err != nil {
		return library.Statistics{}, err
	}
	return b.lib.Statistics()
}

func (b *Backend) ListSubscriptionUpdatesPage(subID string, limit, offset int) ([]subscription.UpdateItem, error) {
	if err := b.requireLogin(); err != nil {
		return nil, err
	}
	return b.subs.ListUpdatesPage(subID, limit, offset)
}

func (b *Backend) UserDynamicsPage(ctx context.Context, mid int64, offset string) (bilibili.UserDynamicsPage, error) {
	if err := b.requireLogin(); err != nil {
		return bilibili.UserDynamicsPage{}, err
	}
	return b.bili.UserDynamics(ctx, mid, offset)
}

func (b *Backend) UserVideosPage(ctx context.Context, mid int64, page int) (bilibili.UserVideosPage, error) {
	if err := b.requireLogin(); err != nil {
		return bilibili.UserVideosPage{}, err
	}
	return b.bili.UserVideos(ctx, mid, page, 30, "pubdate", "")
}

// LiveStatus is a read-only refresh of the visible accounts, with a 60s cache.
// It never starts automatic recordings in response to a UI request.
func (b *Backend) LiveStatus(ctx context.Context, uids []int64) ([]live.RoomState, error) {
	if err := b.requireLogin(); err != nil {
		return nil, err
	}
	return b.live.RefreshForAccounts(ctx, uids)
}
