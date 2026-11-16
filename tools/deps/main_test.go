package main

import "testing"

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
