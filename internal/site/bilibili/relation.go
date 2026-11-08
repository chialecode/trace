package bilibili

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// RelationTag describes a Bilibili following group (tag).
type RelationTag struct {
	TagID int64  `json:"tagid"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

// FollowingUser describes a creator in the user's following list.
type FollowingUser struct {
	Mid     int64  `json:"mid"`
	Uname   string `json:"uname"`
	Face    string `json:"face"`
	Sign    string `json:"sign"`
	Special int    `json:"special,omitempty"`
}

// FollowingsPage holds a page of following users.
type FollowingsPage struct {
	Total int             `json:"total"`
	Items []FollowingUser `json:"items"`
}

// RelationTags fetches all follow groups for the logged-in user from /x/relation/tags.
func (c *Client) RelationTags(ctx context.Context) ([]RelationTag, error) {
	if err := c.RequireAuth("/x/relation/tags"); err != nil {
		return nil, err
	}
	fullURL := strings.TrimRight(c.endpoints.APIBase, "/") + "/x/relation/tags"
	var raw []RelationTag
	if _, err := c.doBiliJSON(ctx, http.MethodGet, fullURL, nil, "", true, &raw); err != nil {
		return nil, err
	}
	return raw, nil
}

// RelationTagUsers fetches users in a specific follow group from /x/relation/tag.
func (c *Client) RelationTagUsers(ctx context.Context, mid int64, tagID int64, pn, ps int) ([]FollowingUser, error) {
	if err := c.RequireAuth("/x/relation/tag"); err != nil {
		return nil, err
	}
	if pn <= 0 {
		pn = 1
	}
	if ps <= 0 || ps > 50 {
		ps = 50
	}
	q := url.Values{}
	if mid > 0 {
		q.Set("mid", strconv.FormatInt(mid, 10))
	}
	q.Set("tagid", strconv.FormatInt(tagID, 10))
	q.Set("pn", strconv.Itoa(pn))
	q.Set("ps", strconv.Itoa(ps))
	fullURL := strings.TrimRight(c.endpoints.APIBase, "/") + "/x/relation/tag?" + q.Encode()

	var raw []FollowingUser
	if _, err := c.doBiliJSON(ctx, http.MethodGet, fullURL, nil, "", true, &raw); err != nil {
		return nil, err
	}
	for i := range raw {
		raw[i].Face = normalizeHTTPSURL(raw[i].Face)
	}
	return raw, nil
}

// RelationFollowings fetches a page of the user's followings from /x/relation/followings.
func (c *Client) RelationFollowings(ctx context.Context, vmid int64, pn, ps int) (FollowingsPage, error) {
	if err := c.RequireAuth("/x/relation/followings"); err != nil {
		return FollowingsPage{}, err
	}
	if vmid <= 0 && c.credStore != nil {
		vmid = c.credStore.Status().Mid
	}
	if pn <= 0 {
		pn = 1
	}
	if ps <= 0 || ps > 50 {
		ps = 50
	}

	q := url.Values{}
	q.Set("vmid", strconv.FormatInt(vmid, 10))
	q.Set("pn", strconv.Itoa(pn))
	q.Set("ps", strconv.Itoa(ps))
	q.Set("order", "desc")
	q.Set("order_type", "attention")
	fullURL := strings.TrimRight(c.endpoints.APIBase, "/") + "/x/relation/followings?" + q.Encode()

	var raw struct {
		List  []FollowingUser `json:"list"`
		Total int             `json:"total"`
	}
	if _, err := c.doBiliJSON(ctx, http.MethodGet, fullURL, nil, "", true, &raw); err != nil {
		return FollowingsPage{}, err
	}
	for i := range raw.List {
		raw.List[i].Face = normalizeHTTPSURL(raw.List[i].Face)
	}
	return FollowingsPage{
		Total: raw.Total,
		Items: raw.List,
	}, nil
}
