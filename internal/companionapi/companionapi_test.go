package companionapi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestPairPayloadEncodeDecodeRoundTrip(t *testing.T) {
	fp := strings.Repeat("a1b2c3d4", 8)
	in := PairPayload{
		Version:     PairPayloadVersion,
		Node:        "node-12345",
		Name:        "Workstation",
		Channel:     "release", // alias should normalize to "stable"
		Port:        DefaultStablePort,
		Addrs:       []string{"192.168.1.20", " 192.168.1.20 ", "[fd00::1]", "100.64.0.5"},
		Fingerprint: strings.ToUpper(fp[:8]) + ":" + fp[8:],
		Code:        "abcdef0123456789abcdef0123456789",
	}

	encoded, err := EncodePairPayload(in)
	if err != nil {
		t.Fatalf("EncodePairPayload failed: %v", err)
	}
	if !strings.HasPrefix(encoded, PairPayloadPrefix) {
		t.Fatalf("encoded payload missing prefix %q: %q", PairPayloadPrefix, encoded)
	}
	if strings.Contains(encoded, "=") {
		t.Fatalf("expected unpadded base64url, got %q", encoded)
	}

	got, err := DecodePairPayload(encoded)
	if err != nil {
		t.Fatalf("DecodePairPayload failed: %v", err)
	}
	if got.Version != 1 {
		t.Errorf("Version = %d, want 1", got.Version)
	}
	if got.Node != "node-12345" || got.Name != "Workstation" {
		t.Errorf("Node/Name = %q/%q", got.Node, got.Name)
	}
	if got.Channel != ChannelStable {
		t.Errorf("Channel = %q, want %q", got.Channel, ChannelStable)
	}
	if got.Port != DefaultStablePort {
		t.Errorf("Port = %d, want %d", got.Port, DefaultStablePort)
	}
	if len(got.Addrs) != 3 || got.Addrs[0] != "192.168.1.20" || got.Addrs[1] != "fd00::1" || got.Addrs[2] != "100.64.0.5" {
		t.Errorf("Addrs = %#v", got.Addrs)
	}
	if got.Fingerprint != fp {
		t.Errorf("Fingerprint = %q, want %q", got.Fingerprint, fp)
	}
	if got.Code != in.Code {
		t.Errorf("Code = %q, want %q", got.Code, in.Code)
	}

	// Trailing padding in base64 should still decode gracefully.
	if _, err := DecodePairPayload("  " + encoded + "==\n"); err != nil {
		t.Errorf("DecodePairPayload with padding/whitespace failed: %v", err)
	}
}

func TestPairPayloadDecodeErrors(t *testing.T) {
	validFP := strings.Repeat("01234567", 8)
	badPayloads := []struct {
		name string
		p    PairPayload
	}{
		{"bad version", PairPayload{Version: 2, Node: "n", Channel: ChannelStable, Port: 47381, Addrs: []string{"192.168.1.2"}, Fingerprint: validFP, Code: "abc"}},
		{"missing node", PairPayload{Version: 1, Node: " ", Channel: ChannelStable, Port: 47381, Addrs: []string{"192.168.1.2"}, Fingerprint: validFP, Code: "abc"}},
		{"test channel rejected", PairPayload{Version: 1, Node: "n", Channel: "test", Port: 47381, Addrs: []string{"192.168.1.2"}, Fingerprint: validFP, Code: "abc"}},
		{"port zero", PairPayload{Version: 1, Node: "n", Channel: ChannelStable, Port: 0, Addrs: []string{"192.168.1.2"}, Fingerprint: validFP, Code: "abc"}},
		{"port overflow", PairPayload{Version: 1, Node: "n", Channel: ChannelStable, Port: 70000, Addrs: []string{"192.168.1.2"}, Fingerprint: validFP, Code: "abc"}},
		{"empty addrs", PairPayload{Version: 1, Node: "n", Channel: ChannelStable, Port: 47381, Addrs: nil, Fingerprint: validFP, Code: "abc"}},
		{"short fp", PairPayload{Version: 1, Node: "n", Channel: ChannelStable, Port: 47381, Addrs: []string{"192.168.1.2"}, Fingerprint: "abcd", Code: "abc"}},
		{"missing code", PairPayload{Version: 1, Node: "n", Channel: ChannelStable, Port: 47381, Addrs: []string{"192.168.1.2"}, Fingerprint: validFP, Code: ""}},
		{"whitespace in code", PairPayload{Version: 1, Node: "n", Channel: ChannelStable, Port: 47381, Addrs: []string{"192.168.1.2"}, Fingerprint: validFP, Code: "a b"}},
	}
	for _, tc := range badPayloads {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := EncodePairPayload(tc.p); !errors.Is(err, ErrInvalidPairPayload) {
				t.Fatalf("EncodePairPayload(%s) err = %v, want ErrInvalidPairPayload", tc.name, err)
			}
		})
	}

	rawBad := []string{
		"",
		"trace-remote:1:abc",
		"trace-pair:",
		"trace-pair:!!!not-base64!!!",
		"trace-pair:e30", // {} missing required fields
	}
	for _, raw := range rawBad {
		if _, err := DecodePairPayload(raw); !errors.Is(err, ErrInvalidPairPayload) {
			t.Errorf("DecodePairPayload(%q) err = %v, want ErrInvalidPairPayload", raw, err)
		}
	}
}

func TestFingerprintAndIdentityCode(t *testing.T) {
	der := []byte("synthetic-leaf-certificate-der")
	wantSum := sha256.Sum256(der)
	wantFP := hex.EncodeToString(wantSum[:])

	gotFP := Fingerprint(der)
	if gotFP != wantFP {
		t.Fatalf("Fingerprint = %q, want %q", gotFP, wantFP)
	}

	// Known vector matching design §4.2 example format.
	exampleFP := "3f7a91c20b5ed844" + strings.Repeat("0", 48)
	idCode, err := IdentityCode(exampleFP)
	if err != nil {
		t.Fatalf("IdentityCode failed: %v", err)
	}
	if idCode != "3F7A-91C2-0B5E-D844" {
		t.Fatalf("IdentityCode = %q, want 3F7A-91C2-0B5E-D844", idCode)
	}

	// Accepts colons, dashes, and spaces.
	idCode2, err := IdentityCode("3F:7A-91C2 0B5E-D844" + strings.Repeat("0", 48))
	if err != nil || idCode2 != "3F7A-91C2-0B5E-D844" {
		t.Fatalf("IdentityCode formatted = %q, err = %v", idCode2, err)
	}

	if _, err := IdentityCode("not-a-valid-fp"); err == nil {
		t.Fatalf("IdentityCode accepted invalid fingerprint")
	}
}

func TestShortCodeNormalizeAndFormat(t *testing.T) {
	norm, ok := NormalizeShortCode(" 3f7k-9q2m ")
	if !ok || norm != "3F7K9Q2M" {
		t.Fatalf("NormalizeShortCode = (%q, %v), want (3F7K9Q2M, true)", norm, ok)
	}
	if formatted := FormatShortCode("3f7k9q2m"); formatted != "3F7K-9Q2M" {
		t.Fatalf("FormatShortCode = %q, want 3F7K-9Q2M", formatted)
	}

	// Reject ambiguous characters (0, O, 1, I) and wrong length.
	for _, bad := range []string{"", "3F7K9Q2", "3F7K9Q2M9", "0F7K-9Q2M", "OF7K-9Q2M", "1F7K-9Q2M", "IF7K-9Q2M"} {
		if _, ok := NormalizeShortCode(bad); ok {
			t.Errorf("NormalizeShortCode(%q) = true, want false", bad)
		}
	}
}

func TestChannelAndDefaultPorts(t *testing.T) {
	if ch, ok := NormalizeChannel("stable"); !ok || ch != ChannelStable {
		t.Errorf("stable -> (%q, %v)", ch, ok)
	}
	if ch, ok := NormalizeChannel("release"); !ok || ch != ChannelStable {
		t.Errorf("release -> (%q, %v)", ch, ok)
	}
	if ch, ok := NormalizeChannel("dev"); !ok || ch != ChannelDev {
		t.Errorf("dev -> (%q, %v)", ch, ok)
	}
	if ch, ok := NormalizeChannel("source"); !ok || ch != ChannelDev {
		t.Errorf("source -> (%q, %v)", ch, ok)
	}
	if _, ok := NormalizeChannel("test"); ok {
		t.Errorf("test channel should be rejected")
	}
	if got := DefaultPortForChannel(ChannelStable); got != 47381 {
		t.Errorf("DefaultPortForChannel(stable) = %d, want 47381", got)
	}
	if got := DefaultPortForChannel(ChannelDev); got != 47382 {
		t.Errorf("DefaultPortForChannel(dev) = %d, want 47382", got)
	}
}

func TestPathHelpersAndJSONContracts(t *testing.T) {
	if got := MediaPath("bilibili:video:BV1xx", `images\01.jpg`); got != "/api/v1/media/bilibili:video:BV1xx/images/01.jpg" {
		t.Errorf("MediaPath = %q", got)
	}
	if got := LibraryItemPath("weibo:dynamic:123"); got != "/api/v1/library/items/weibo:dynamic:123" {
		t.Errorf("LibraryItemPath = %q", got)
	}
	if got := AvatarPath("douyin", "MS4wLjAB"); got != "/api/v1/avatars/douyin/MS4wLjAB" {
		t.Errorf("AvatarPath = %q", got)
	}
	if got := DownloadTaskActionPath("task-1", "pause"); got != "/api/v1/tasks/downloads/task-1/pause" {
		t.Errorf("DownloadTaskActionPath = %q", got)
	}
	if got := CacheQueueActionPath("q-1", "resume"); got != "/api/v1/tasks/queues/q-1/resume" {
		t.Errorf("CacheQueueActionPath = %q", got)
	}
	if got := LiveRecordingStopPath("bilibili:live:100"); got != "/api/v1/live/recordings/bilibili:live:100/stop" {
		t.Errorf("LiveRecordingStopPath = %q", got)
	}

	// Verify ErrorBody JSON shape matches protocol documentation.
	var eb ErrorBody
	eb.Error = ErrorDetail{
		Code:           CodeProtocolMismatch,
		Message:        "协议版本不兼容",
		ServerVersion:  "0.1.2",
		ServerProtocol: ProtocolVersion,
		ClientVersion:  "0.1.0",
		ClientProtocol: "0",
	}
	raw, err := json.Marshal(eb)
	if err != nil {
		t.Fatalf("Marshal ErrorBody: %v", err)
	}
	var decoded ErrorBody
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("Unmarshal ErrorBody: %v", err)
	}
	if decoded.Error != eb.Error {
		t.Fatalf("ErrorBody roundtrip mismatch: got %+v, want %+v", decoded.Error, eb.Error)
	}

	// Verify EventFrame bye shape.
	ev := EventFrame{Type: EventBye, Reason: ByeReasonExit, Timestamp: "2026-11-26T12:00:00Z"}
	rawEv, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("Marshal EventFrame: %v", err)
	}
	var decEv EventFrame
	if err := json.Unmarshal(rawEv, &decEv); err != nil {
		t.Fatalf("Unmarshal EventFrame: %v", err)
	}
	if decEv != ev {
		t.Fatalf("EventFrame roundtrip mismatch: got %+v, want %+v", decEv, ev)
	}
}
