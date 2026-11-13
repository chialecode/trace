package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chialecode/trace/internal/sitecheck"
)

func TestAct011FollowupHappyPathQuotasAndRedaction(t *testing.T) {
	env := setupAct010MockEnv(t, true)
	outPath := filepath.Join(env.rootDir, "artifacts", "site-check", "act011.json")

	rc, err := runAct011(context.Background(), act010Config{
		RootDir:        env.rootDir,
		OutPath:        outPath,
		RecordDuration: 5 * time.Second,
		MinSpacing:     -1,
		Endpoints:      env.endpoints,
		AllowLocalhost: true,
	})
	if err != nil {
		t.Fatalf("runAct011 failed: %v (steps=%+v)", err, rc.Steps)
	}
	if !rc.Passed || rc.StoppedBy != "" || !rc.TempMediaDeleted || !rc.SecretScanClean {
		t.Fatalf("unexpected act011 receipt state: %+v", rc)
	}
	if rc.APIUsage.APICalls > act011MaxAPICalls {
		t.Fatalf("API calls=%d exceeded act011 cap %d", rc.APIUsage.APICalls, act011MaxAPICalls)
	}
	if rc.APIUsage.Categories["profile_self"] != 1 || rc.APIUsage.Categories["reflow_info"] != 1 || rc.APIUsage.Categories["web_enter"] != 1 {
		t.Fatalf("unexpected act011 API category counts: %+v", rc.APIUsage.Categories)
	}
	if rc.VODMedia.Requests != 0 {
		t.Fatalf("expected 0 VOD media requests in act011, got %d", rc.VODMedia.Requests)
	}

	// Verify V-06 room_id=2 and V-07 FLV + HLS inspection populated.
	if _, ok := rc.Verifications["V-06"]; !ok {
		t.Fatal("missing V-06 in act011 receipt")
	}
	v07, _ := rc.Verifications["V-07"].(map[string]any)
	if v07["status"] != "completed" || v07["wireVideoTagFormat"] != "hevc-hvc1" || v07["moovBeforeMdat"] != true {
		t.Fatalf("unexpected V-07 in act011 receipt: %+v", v07)
	}

	// Verify usage-act010.json was NEVER created/touched by runAct011, while usage-act011.json exists.
	if _, statErr := os.Stat(filepath.Join(env.rootDir, ".local", "site-check", "usage-act010.json")); !os.IsNotExist(statErr) {
		t.Fatal("runAct011 unexpectedly created or touched usage-act010.json")
	}
	if _, statErr := os.Stat(filepath.Join(env.rootDir, ".local", "site-check", "usage-act011.json")); statErr != nil {
		t.Fatalf("missing usage-act011.json: %v", statErr)
	}

	// Verify 2nd run of act011 is refused before any HTTP request.
	beforeReqs := env.totalRequests.Load()
	if _, err := runAct011(context.Background(), act010Config{
		RootDir: env.rootDir, OutPath: outPath, RecordDuration: 5 * time.Second,
		MinSpacing: -1, Endpoints: env.endpoints, AllowLocalhost: true,
	}); !errors.Is(err, sitecheck.ErrQuotaExceeded) {
		t.Fatalf("expected ErrQuotaExceeded on 2nd act011 run, got: %v", err)
	}
	if env.totalRequests.Load() != beforeReqs {
		t.Fatal("2nd act011 run issued HTTP requests despite MaxRuns=1")
	}

	// Verify receipt file on disk has zero secrets or IDs.
	rawBytes, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	rawStr := string(rawBytes)
	if findings := sitecheck.ScanTextForSecrets(rawStr); len(findings) > 0 {
		t.Fatalf("secret scan findings in act011.json: %v", findings)
	}
	for _, tok := range []string{"SENTINEL_SESSION_010", synthSecUIDUser1, synthSecUIDUser2, synthShortID2, synthRoomIDLive} {
		if strings.Contains(rawStr, tok) {
			t.Fatalf("act011.json leaked forbidden token %q", tok)
		}
	}
}

func TestSavedReceiptRedactionWhenPresent(t *testing.T) {
	repoRoot := filepath.Join("..", "..")
	receiptPath := filepath.Join(repoRoot, "artifacts", "site-check", "act010.json")
	rawBytes, err := os.ReadFile(receiptPath)
	if os.IsNotExist(err) {
		t.Skip("artifacts/site-check/act010.json not present")
	}
	if err != nil {
		t.Fatalf("read act010.json: %v", err)
	}
	var rc act010Receipt
	if err := json.Unmarshal(rawBytes, &rc); err != nil {
		t.Fatalf("unmarshal act010.json: %v", err)
	}
	red := newAct010Redactor()
	if rawCookie, cleaned, cErr := readAndValidateDouyinCookie(repoRoot); cErr == nil {
		red.addCookieHeader(rawCookie)
		red.addCookieMap(cleaned)
	}
	if tg, tErr := loadAndValidateAct010Targets(repoRoot); tErr == nil {
		for _, u := range tg.Users {
			red.addIDs(u.SecUID)
			red.addURLs(u.URL)
		}
		for _, w := range tg.Works {
			red.addIDs(w.AwemeID)
			red.addURLs(w.URL)
		}
		for _, lr := range tg.LiveRooms {
			red.addIDs(lr.WebRID)
			red.addURLs(lr.URL)
		}
	}
	if err := red.verifyJSONClean(string(rawBytes), &rc); err != nil {
		t.Fatalf("saved act010.json failed redaction check: %v", err)
	}
}

// tengine302Body mirrors the small text/html page CDN edges send with a 302.
const tengine302Body = "<!DOCTYPE HTML PUBLIC \"-//IETF//DTD HTML 2.0//EN\">\n<html>\n<head><title>302 Found</title></head>\n<body>\n<h1>302 Found</h1>\n<p>The requested resource has been temporarily moved to a new location.</p>\n<hr/>Powered by Tengine</body>\n</html>\n"

func TestLiveFLVRedirectWithHTMLBodyIsNotAVerifyPage(t *testing.T) {
	env := setupAct010MockEnv(t, true)
	env.liveFLVHook = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Location", "/cdn/live/edge.flv?expire=1&sign=synth")
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusFound)
		_, _ = w.Write([]byte(tengine302Body))
		return true
	}
	outPath := filepath.Join(env.rootDir, "artifacts", "site-check", "act012.json")
	rc, err := runLiveFollowup(context.Background(), act012Followup, act010Config{
		RootDir: env.rootDir, OutPath: outPath, RecordDuration: 5 * time.Second,
		MinSpacing: -1, Endpoints: env.endpoints, AllowLocalhost: true,
	})
	if err != nil {
		t.Fatalf("act012 with a CDN 302 failed: %v (steps=%+v)", err, rc.Steps)
	}
	if rc.StoppedBy != "" || !rc.Passed {
		t.Fatalf("a 302 with an HTML body must not stop the check: %+v", rc)
	}
	if len(rc.Redirects) != 1 || rc.Redirects[0]["status"] != http.StatusFound ||
		rc.Redirects[0]["targetSameHost"] != true || rc.Redirects[0]["targetHostKind"] != "ip" {
		t.Fatalf("unexpected redirect shapes: %+v", rc.Redirects)
	}
	if rc.LiveMedia.Requests != 2 {
		t.Fatalf("expected 2 FLV requests (302 + edge), got %d", rc.LiveMedia.Requests)
	}
	if rc.APIUsage.Categories["reflow_info"] != 0 || rc.APIUsage.Categories["profile_self"] != 1 {
		t.Fatalf("act012 must skip reflow/info: %+v", rc.APIUsage.Categories)
	}
	if _, ok := rc.Verifications["V-06"]; ok {
		t.Fatal("act012 must not run the V-06 offline probe")
	}
	for _, name := range []string{"usage-act010.json", "usage-act011.json"} {
		if _, statErr := os.Stat(filepath.Join(env.rootDir, ".local", "site-check", name)); !os.IsNotExist(statErr) {
			t.Fatalf("act012 touched %s", name)
		}
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sign=synth") || strings.Contains(string(raw), "edge.flv") {
		t.Fatalf("receipt kept the redirect target: %s", raw)
	}
}

func TestLiveFLVRedirectOutsideProductionPolicyIsReported(t *testing.T) {
	env := setupAct010MockEnv(t, true)
	env.liveFLVHook = func(w http.ResponseWriter, r *http.Request) bool {
		w.Header().Set("Location", "http://cdn.example.invalid/live/edge.flv")
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusFound)
		_, _ = w.Write([]byte(tengine302Body))
		return true
	}
	rc, err := runLiveFollowup(context.Background(), act012Followup, act010Config{
		RootDir: env.rootDir, OutPath: filepath.Join(env.rootDir, "act012.json"), RecordDuration: 5 * time.Second,
		MinSpacing: -1, Endpoints: env.endpoints, AllowLocalhost: true,
	})
	if err == nil || rc.Passed {
		t.Fatalf("a redirect the recorder refuses must fail the step: %+v", rc)
	}
	if rc.StoppedBy != "" {
		t.Fatalf("a refused redirect is a finding, not a stop condition: %q", rc.StoppedBy)
	}
	var v07 step
	for _, s := range rc.Steps {
		if s.Name == "v07_live_enter_and_record" {
			v07 = s
		}
	}
	if v07.Facts["productionRedirectBlocked"] == nil {
		t.Fatalf("missing productionRedirectBlocked fact: %+v", v07)
	}
	if len(rc.Redirects) == 0 || rc.Redirects[0]["targetScheme"] != "http" || rc.Redirects[0]["targetHostKind"] != "domain" {
		t.Fatalf("unexpected redirect shapes: %+v", rc.Redirects)
	}
	if rc.LiveMedia.Requests > act012Followup.maxLiveReqs {
		t.Fatalf("FLV requests %d exceed %d", rc.LiveMedia.Requests, act012Followup.maxLiveReqs)
	}
}

func TestAct014RepeatsAct012BudgetOnItsOwnLedger(t *testing.T) {
	if act014Followup.id == act012Followup.id || act014Followup.id != "act014" {
		t.Fatalf("act014 must use its own ledger, got %q", act014Followup.id)
	}
	if act014Followup.maxAPICalls != 3 || act014Followup.maxLiveReqs != 4 || act014Followup.maxHLSReqs != 0 || act014Followup.offlineProbe {
		t.Fatalf("act014 budget drifted: %+v", act014Followup)
	}
	if _, ok := act014Followup.apiLimits["reflow_info"]; ok {
		t.Fatal("act014 must not call reflow/info")
	}
}
