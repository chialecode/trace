package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestEscapePath(t *testing.T) {
	if got := escapePath("github.com/Eyevinn/mp4ff"); got != "github.com/!eyevinn/mp4ff" {
		t.Fatalf("escapePath = %q", got)
	}
}

func TestStable(t *testing.T) {
	for v, want := range map[string]bool{
		"v1.2.3": true, "1.27.4": true,
		"v1.2.3-rc.1": false, "v0.0.0-20260101000000-abcdef123456": false, "v2.0.0+incompatible": false, "v1.2": false,
	} {
		if _, ok := stable(v); ok != want {
			t.Errorf("stable(%q) = %v, want %v", v, ok, want)
		}
	}
	a, _ := stable("v0.9.10")
	b, _ := stable("v0.10.0")
	if !less(a, b) || less(b, a) {
		t.Fatal("versions must compare by number, not as text")
	}
}

func TestReviewByExpired(t *testing.T) {
	now := time.Date(2026, 11, 20, 15, 30, 0, 0, time.UTC)
	cases := []struct {
		name        string
		reviewBy    string
		wantExpired bool
	}{
		{"future date valid", "2026-12-01", false},
		{"today valid", "2026-11-20", false},
		{"yesterday expired", "2026-11-19", true},
		{"empty expired", "", true},
		{"TBD malformed rejected", "TBD", true},
		{"never malformed rejected", "never", true},
		{"invalid calendar date rejected", "2026-13-40", true},
		{"wrong format rejected", "2026/12/01", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, reason := reviewByExpired(tc.reviewBy, now)
			if got != tc.wantExpired {
				t.Fatalf("reviewByExpired(%q) = (%v, %q), want %v", tc.reviewBy, got, reason, tc.wantExpired)
			}
		})
	}
}

func TestParseGoProxy(t *testing.T) {
	cases := []struct {
		raw  string
		want string
	}{
		{"", defaultGoProxy},
		{"direct", defaultGoProxy},
		{"off", defaultGoProxy},
		{"https://goproxy.cn,direct", "https://goproxy.cn"},
		{"https://goproxy.cn/|https://proxy.golang.org,direct", "https://goproxy.cn"},
		{"file:///tmp/cache,http://127.0.0.1:8080/proxy/,direct", "http://127.0.0.1:8080/proxy"},
		{"direct,https://proxy.example.com/go", "https://proxy.example.com/go"},
	}
	for _, tc := range cases {
		if got := parseGoProxy(tc.raw); got != tc.want {
			t.Errorf("parseGoProxy(%q) = %q, want %q", tc.raw, got, tc.want)
		}
	}
}

func TestNewestMatureUsesConfiguredProxy(t *testing.T) {
	now := time.Now().UTC()
	recent := now.Add(-2 * 24 * time.Hour).Format(time.RFC3339)
	mature := now.Add(-10 * 24 * time.Hour).Format(time.RFC3339)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/example.com/!mod/@v/list":
			fmt.Fprintln(w, "v1.0.0")
			fmt.Fprintln(w, "v1.1.0")
			fmt.Fprintln(w, "v1.2.0")
			fmt.Fprintln(w, "v1.3.0-rc.1")
		case "/example.com/!mod/@v/v1.2.0.info":
			fmt.Fprintf(w, `{"Version":"v1.2.0","Time":%q}`, recent)
		case "/example.com/!mod/@v/v1.1.0.info":
			fmt.Fprintf(w, `{"Version":"v1.1.0","Time":%q}`, mature)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	gotMature, gotNewest, err := newestMature(srv.URL, "example.com/Mod", "v1.0.0", moduleTime(srv.URL, "example.com/Mod"))
	if err != nil {
		t.Fatalf("newestMature: %v", err)
	}
	if gotNewest == nil || gotNewest.version != "v1.2.0" {
		t.Fatalf("newest = %+v, want v1.2.0", gotNewest)
	}
	if gotMature == nil || gotMature.version != "v1.1.0" || gotMature.days < minAgeDays {
		t.Fatalf("mature = %+v, want v1.1.0 (>= %d days)", gotMature, minAgeDays)
	}
}
