package appdirs

import "testing"

// A build without injected build information (go run, go test, mygo dev)
// reports 0.0.0-dev; an injected one reports exactly what was injected.
func TestBuildInfoDefaultsAndInjected(t *testing.T) {
	saved := [4]string{version, commit, buildTime, channel}
	t.Cleanup(func() { version, commit, buildTime, channel = saved[0], saved[1], saved[2], saved[3] })

	version, commit, buildTime = "", "", ""
	got := BuildInfo()
	if UnversionedBuild != "0.0.0-dev" {
		t.Fatalf("UnversionedBuild = %q, want 0.0.0-dev", UnversionedBuild)
	}
	if got.Version != UnversionedBuild || got.Commit != "" || got.BuildTime != "" {
		t.Fatalf("uninjected build = %+v, want version 0.0.0-dev and no commit or time", got)
	}

	version, commit, buildTime, channel = " 0.1.2 ", "1a2b3c4d5e6f", "2026-11-09T15:30:00Z", "stable"
	got = BuildInfo()
	if got.Version != "0.1.2" || got.Commit != "1a2b3c4d5e6f" || got.BuildTime != "2026-11-09T15:30:00Z" {
		t.Fatalf("injected build = %+v", got)
	}
	if CompiledChannel() != "stable" {
		t.Fatalf("compiled channel = %q, want stable", CompiledChannel())
	}
}
