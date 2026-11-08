package bilibili

import (
	"context"

	"github.com/chialecode/trace/internal/credential"
)

// LoginWithCookie validates and replaces a manual credential as one sequence,
// so an in-flight renewal cannot later undo a login or logout.
func (c *Client) LoginWithCookie(ctx context.Context, rawCookie, rawRefreshToken string) (credential.Status, error) {
	ctx, unlock := c.beginSequence(ctx, true)
	defer unlock()
	cookies, refresh, err := credential.ParseManualCookieInput(rawCookie, rawRefreshToken)
	if err != nil {
		return c.currentAccountStatus(), err
	}
	mid, name, face, err := c.ValidateCandidateCookies(ctx, cookies)
	if err != nil {
		return c.currentAccountStatus(), err
	}
	if err := ctx.Err(); err != nil {
		return c.currentAccountStatus(), err
	}
	c.prepareAccountReplacement(mid)
	status, err := c.credStore.SaveVerifiedManual(cookies, refresh, mid, name, face)
	if err == nil {
		c.CancelQR("")
		c.ClearCaches()
	}
	return status, err
}

func (c *Client) SetAccountReplacingHandler(handler func()) {
	c.automationMu.Lock()
	c.accountReplacing = handler
	c.automationMu.Unlock()
}

func (c *Client) prepareAccountReplacement(mid int64) {
	previous := c.currentAccountStatus()
	if !previous.LoggedIn || previous.Mid == 0 || previous.Mid == mid {
		return
	}
	c.automationMu.RLock()
	handler := c.accountReplacing
	c.automationMu.RUnlock()
	if handler != nil {
		handler()
	}
}

func (c *Client) LogoutCredential() error {
	_, unlock := c.beginSequence(context.Background(), true)
	defer unlock()
	c.CancelQR("")
	if err := c.credStore.Clear(); err != nil {
		return err
	}
	c.ClearCaches()
	return nil
}
