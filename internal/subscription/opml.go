package subscription

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// OPMLCreator represents a creator extracted from an OPML 2.0 document.
type OPMLCreator struct {
	Mid     int64  `json:"mid"`
	Uname   string `json:"uname"`
	HTMLURL string `json:"htmlUrl"`
	XMLURL  string `json:"xmlUrl"`
}

type opmlDocument struct {
	XMLName xml.Name `xml:"opml"`
	Version string   `xml:"version,attr"`
	Head    opmlHead `xml:"head"`
	Body    opmlBody `xml:"body"`
}

type opmlHead struct {
	Title       string `xml:"title"`
	DateCreated string `xml:"dateCreated,omitempty"`
}

type opmlBody struct {
	Outlines []opmlOutline `xml:"outline"`
}

type opmlOutline struct {
	Text     string        `xml:"text,attr"`
	Title    string        `xml:"title,attr,omitempty"`
	Type     string        `xml:"type,attr,omitempty"`
	XMLURL   string        `xml:"xmlUrl,attr,omitempty"`
	HTMLURL  string        `xml:"htmlUrl,attr,omitempty"`
	Children []opmlOutline `xml:"outline,omitempty"`
}

var (
	reSpaceMid = regexp.MustCompile(`(?i)space\.bilibili\.com/([1-9][0-9]{0,18})`)
	reFeedMid  = regexp.MustCompile(`(?i)/bilibili/user/(?:video|dynamic|channel)/([1-9][0-9]{0,18})`)
	reQueryMid = regexp.MustCompile(`(?i)(?:host_mid|mid|uid)=([1-9][0-9]{0,18})`)
)

// ExportOPML serializes subscriptions into a clean OPML 2.0 XML document without credentials or local paths (C33).
func ExportOPML(subs []Subscription, now time.Time) ([]byte, error) {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	outlines := make([]opmlOutline, 0, len(subs))
	seen := make(map[int64]bool, len(subs))
	for _, s := range subs {
		if s.Mid <= 0 || seen[s.Mid] {
			continue
		}
		seen[s.Mid] = true
		name := strings.TrimSpace(s.Uname)
		if name == "" {
			name = fmt.Sprintf("UID %d", s.Mid)
		}
		outlines = append(outlines, opmlOutline{
			Text:    name,
			Title:   name,
			Type:    "rss",
			XMLURL:  fmt.Sprintf("https://api.bilibili.com/x/polymer/web-dynamic/v1/feed/space?host_mid=%d", s.Mid),
			HTMLURL: fmt.Sprintf("https://space.bilibili.com/%d", s.Mid),
		})
	}

	doc := opmlDocument{
		Version: "2.0",
		Head: opmlHead{
			Title:       "TRACE Bilibili Subscriptions",
			DateCreated: now.UTC().Format(time.RFC1123Z),
		},
		Body: opmlBody{
			Outlines: outlines,
		},
	}

	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return nil, fmt.Errorf("subscription: encode OPML: %w", err)
	}
	buf.WriteByte('\n')
	return buf.Bytes(), nil
}

// ParseOPML parses an OPML 2.0 XML document and extracts deduplicated Bilibili creators (C33).
func ParseOPML(raw []byte) ([]OPMLCreator, error) {
	var doc opmlDocument
	if err := xml.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("subscription: invalid OPML XML: %w", err)
	}

	var out []OPMLCreator
	seen := make(map[int64]bool)

	var walk func(items []opmlOutline)
	walk = func(items []opmlOutline) {
		for _, item := range items {
			if mid := extractMidFromOutline(item); mid > 0 && !seen[mid] {
				seen[mid] = true
				name := strings.TrimSpace(item.Title)
				if name == "" {
					name = strings.TrimSpace(item.Text)
				}
				if name == "" {
					name = fmt.Sprintf("UID %d", mid)
				}
				out = append(out, OPMLCreator{
					Mid:     mid,
					Uname:   name,
					HTMLURL: fmt.Sprintf("https://space.bilibili.com/%d", mid),
					XMLURL:  item.XMLURL,
				})
			}
			if len(item.Children) > 0 {
				walk(item.Children)
			}
		}
	}

	walk(doc.Body.Outlines)
	return out, nil
}

func extractMidFromOutline(o opmlOutline) int64 {
	candidates := []string{o.HTMLURL, o.XMLURL, o.Text, o.Title}
	for _, c := range candidates {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if m := reSpaceMid.FindStringSubmatch(c); len(m) == 2 {
			if id, err := strconv.ParseInt(m[1], 10, 64); err == nil && id > 0 {
				return id
			}
		}
		if m := reFeedMid.FindStringSubmatch(c); len(m) == 2 {
			if id, err := strconv.ParseInt(m[1], 10, 64); err == nil && id > 0 {
				return id
			}
		}
		if u, err := url.Parse(c); err == nil {
			for _, key := range []string{"host_mid", "mid", "uid", "vmid"} {
				if val := u.Query().Get(key); val != "" {
					if id, err := strconv.ParseInt(val, 10, 64); err == nil && id > 0 {
						return id
					}
				}
			}
		}
		if m := reQueryMid.FindStringSubmatch(c); len(m) == 2 {
			if id, err := strconv.ParseInt(m[1], 10, 64); err == nil && id > 0 {
				return id
			}
		}
	}
	return 0
}
