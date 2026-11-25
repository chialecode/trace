package guard

import (
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/asn1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math/big"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Fixed synthetic P-256 test vector scalar (d = 0x010203...20, purely synthetic, never used in production).
const (
	synthVectorScalarHex      = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"
	synthVectorTicket         = "hash.SYNTHETIC_TICKET_VECTOR_01=="
	synthVectorTsSign         = "ts.2.synthetic_ts_sign_vector_01"
	synthVectorPath           = "/aweme/v1/web/aweme/favorite/"
	synthVectorTimestamp      = int64(1763000000)
	synthVectorPreimage       = "ticket=hash.SYNTHETIC_TICKET_VECTOR_01==&path=/aweme/v1/web/aweme/favorite/&timestamp=1763000000"
	synthVectorPubSEC1B64     = "BFFcPW6545a5BNP+yn9U/c0MwemXvzddylFa0KbDtANfRTa+OlDzGPv5pUdZAqIhUCvvDVfgjFOyzApW8X2fk1Q="
	synthVectorPreimageSHA256 = "7d89cc558d2c5124262021f2e3c39d870f1f013d4e3b24d6197aca12b377ae2c"
	synthVectorGoldenASN1Sign = "MEYCIQCOlz2S8qpetNXEMD1pUprwHSdpdi+XzLAL/ftaNwoMvQIhAPIqAPzWaS8qs6JZH7wyzs4aNb+r5Pi9Wx1XaNOfU1px"
)

func synthVectorPrivateKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	dBytes, err := hex.DecodeString(synthVectorScalarHex)
	if err != nil {
		t.Fatal(err)
	}
	ecdhPriv, err := ecdh.P256().NewPrivateKey(dBytes)
	if err != nil {
		t.Fatal(err)
	}
	rawPub := ecdhPriv.PublicKey().Bytes()
	return &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{
			Curve: elliptic.P256(),
			X:     new(big.Int).SetBytes(rawPub[1:33]),
			Y:     new(big.Int).SetBytes(rawPub[33:65]),
		},
		D: new(big.Int).SetBytes(dBytes),
	}
}

func TestKeyPairGenerationSEC1EncodingAndRoundTrip(t *testing.T) {
	fixedTime := func() time.Time { return time.Unix(synthVectorTimestamp, 0).UTC() }

	st, err := GenerateState(nil, fixedTime)
	if err != nil {
		t.Fatalf("GenerateState failed: %v", err)
	}
	if !st.HasKeyPair() {
		t.Fatalf("generated state missing keypair: %+v", st)
	}
	if st.UpdatedAt != "2025-11-13T02:13:20Z" {
		t.Fatalf("UpdatedAt = %q", st.UpdatedAt)
	}

	rawPub, err := base64.StdEncoding.DecodeString(st.PublicKeySEC1)
	if err != nil {
		t.Fatalf("PublicKeySEC1 invalid base64: %v", err)
	}
	if len(rawPub) != 65 || rawPub[0] != 0x04 {
		t.Fatalf("PublicKeySEC1 decoded len=%d prefix=0x%02x, want 65 & 0x04", len(rawPub), rawPub[0])
	}
	if !strings.HasPrefix(st.PublicKeySEC1, "B") || len(st.PublicKeySEC1) != 88 {
		t.Fatalf("PublicKeySEC1 format unexpected: len=%d prefix=%q", len(st.PublicKeySEC1), st.PublicKeySEC1[:4])
	}

	priv, err := ParsePrivateKey(st.PrivateKeyPKCS8)
	if err != nil {
		t.Fatalf("ParsePrivateKey round-trip failed: %v", err)
	}
	pub, err := DecodePublicKeySEC1(st.PublicKeySEC1)
	if err != nil {
		t.Fatalf("DecodePublicKeySEC1 round-trip failed: %v", err)
	}
	if priv.PublicKey.X.Cmp(pub.X) != 0 || priv.PublicKey.Y.Cmp(pub.Y) != 0 {
		t.Fatal("decoded SEC1 public key does not match private key")
	}

	// Reject malformed inputs.
	if _, err := ParsePrivateKey(""); !errors.Is(err, ErrInvalidPrivateKey) {
		t.Fatalf("empty private key err = %v", err)
	}
	if _, err := ParsePrivateKey("not-base64!!!"); !errors.Is(err, ErrInvalidPrivateKey) {
		t.Fatalf("bad base64 private key err = %v", err)
	}
	if _, err := DecodePublicKeySEC1(base64.StdEncoding.EncodeToString(make([]byte, 65))); !errors.Is(err, ErrInvalidPublicKey) {
		t.Fatalf("zero SEC1 key err = %v", err)
	}
}

func TestDeterministicVectorKeyDerivationAndASN1Signature(t *testing.T) {
	priv := synthVectorPrivateKey(t)
	st, err := StateFromPrivateKey(priv, func() time.Time { return time.Unix(synthVectorTimestamp, 0).UTC() })
	if err != nil {
		t.Fatalf("StateFromPrivateKey failed: %v", err)
	}

	// Verify exact deterministic SEC1 Base64 encoding for the synthetic scalar 0102...1f20.
	if st.PublicKeySEC1 != synthVectorPubSEC1B64 {
		t.Fatalf("PublicKeySEC1 = %q, want %q", st.PublicKeySEC1, synthVectorPubSEC1B64)
	}
	expectedPubB64 := synthVectorPubSEC1B64

	// Check exact preimage formatting (including URL normalization).
	gotPreimage := BuildPreimage(synthVectorTicket, "https://www.douyin.com/aweme/v1/web/aweme/favorite?max_cursor=0", synthVectorTimestamp)
	if gotPreimage != synthVectorPreimage {
		t.Fatalf("BuildPreimage = %q, want %q", gotPreimage, synthVectorPreimage)
	}

	// Handshake-only headers when TsSign is absent.
	hsRes, err := BuildHeaders(BuildHeadersInput{
		State: st,
		Path:  "/aweme/v1/web/user/profile/self/",
		Now:   func() time.Time { return time.Unix(synthVectorTimestamp, 0).UTC() },
	})
	if err != nil {
		t.Fatalf("BuildHeaders handshake failed: %v", err)
	}
	if hsRes.Signed {
		t.Fatal("expected Signed=false when TsSign is empty")
	}
	if hsRes.Headers[HeaderVersion] != "2" ||
		hsRes.Headers[HeaderIterationVersion] != "1" ||
		hsRes.Headers[HeaderWebVersion] != "2" ||
		hsRes.Headers[HeaderREEPublicKey] != expectedPubB64 {
		t.Fatalf("unexpected handshake headers: %+v", hsRes.Headers)
	}
	if _, hasCD := hsRes.Headers[HeaderClientData]; hasCD {
		t.Fatal("handshake without TsSign must not emit bd-ticket-guard-client-data")
	}

	// Full signed headers once TsSign + Ticket are populated.
	st.TsSign = synthVectorTsSign
	st.Ticket = synthVectorTicket
	signedRes, err := BuildHeaders(BuildHeadersInput{
		State: st,
		Path:  synthVectorPath,
		Now:   func() time.Time { return time.Unix(synthVectorTimestamp, 0).UTC() },
	})
	if err != nil {
		t.Fatalf("BuildHeaders signed failed: %v", err)
	}
	if !signedRes.Signed {
		t.Fatal("expected Signed=true when TsSign and Ticket are present")
	}
	if signedRes.Headers[HeaderWebSignType] != "1" {
		t.Fatalf("HeaderWebSignType = %q, want 1", signedRes.Headers[HeaderWebSignType])
	}
	rawCD, err := base64.StdEncoding.DecodeString(signedRes.Headers[HeaderClientData])
	if err != nil {
		t.Fatalf("decode client-data base64: %v", err)
	}
	var payload ClientDataPayload
	if err := json.Unmarshal(rawCD, &payload); err != nil {
		t.Fatalf("unmarshal client-data JSON: %v", err)
	}
	if payload.TsSign != synthVectorTsSign || payload.ReqContent != DefaultReqContent || payload.Timestamp != synthVectorTimestamp {
		t.Fatalf("unexpected payload fields: %+v", payload)
	}

	// Verify ASN.1 DER structure (SEQUENCE { INTEGER r, INTEGER s }) and cryptographic validity.
	derBytes, err := base64.StdEncoding.DecodeString(payload.ReqSign)
	if err != nil {
		t.Fatalf("decode req_sign base64: %v", err)
	}
	if len(derBytes) < 68 || derBytes[0] != 0x30 {
		t.Fatalf("req_sign is not ASN.1 DER SEQUENCE: len=%d first=0x%02x", len(derBytes), derBytes[0])
	}
	var sigStruct struct{ R, S *big.Int }
	rest, err := asn1.Unmarshal(derBytes, &sigStruct)
	if err != nil || len(rest) != 0 || sigStruct.R == nil || sigStruct.S == nil {
		t.Fatalf("invalid ASN.1 ECDSA signature structure: err=%v rest=%d", err, len(rest))
	}
	if !VerifyPreimageASN1(&priv.PublicKey, synthVectorPreimage, payload.ReqSign) {
		t.Fatal("VerifyPreimageASN1 rejected valid signature")
	}
	if VerifyPreimageASN1(&priv.PublicKey, synthVectorPreimage+"x", payload.ReqSign) {
		t.Fatal("VerifyPreimageASN1 accepted tampered preimage")
	}

	// Verify fixed SHA-256 digest and golden ASN.1 DER vector documented in docs/research/douyin.md.
	digest := sha256.Sum256([]byte(synthVectorPreimage))
	if gotHex := hex.EncodeToString(digest[:]); gotHex != synthVectorPreimageSHA256 {
		t.Fatalf("preimage sha256 = %s, want %s", gotHex, synthVectorPreimageSHA256)
	}
	if !VerifyPreimageASN1(&priv.PublicKey, synthVectorPreimage, synthVectorGoldenASN1Sign) {
		t.Fatal("VerifyPreimageASN1 rejected documented golden ASN.1 DER signature vector")
	}

	// FallbackTicket works when State.Ticket is empty but State.TsSign is set.
	stNoTicket := st
	stNoTicket.Ticket = ""
	fbRes, err := BuildHeaders(BuildHeadersInput{
		State:          stNoTicket,
		Path:           synthVectorPath,
		FallbackTicket: "synth_d_ticket_fallback",
		Now:            func() time.Time { return time.Unix(synthVectorTimestamp, 0).UTC() },
	})
	if err != nil || !fbRes.Signed {
		t.Fatalf("FallbackTicket signing failed: signed=%v err=%v", fbRes.Signed, err)
	}
	rawFB, _ := base64.StdEncoding.DecodeString(fbRes.Headers[HeaderClientData])
	var fbPayload ClientDataPayload
	_ = json.Unmarshal(rawFB, &fbPayload)
	if !VerifyPreimageASN1(&priv.PublicKey, BuildPreimage("synth_d_ticket_fallback", synthVectorPath, synthVectorTimestamp), fbPayload.ReqSign) {
		t.Fatal("FallbackTicket signature verification failed")
	}
}

func TestParseServerResponseAndMerge(t *testing.T) {
	serverJSON := `{"ts_sign":"ts.2.synth_srv_ts_01","ticket":"hash.SYNTH_SRV_TICKET_01","client_cert":"SYNTH_CERT_01"}`
	encodedHeader := base64.StdEncoding.EncodeToString([]byte(serverJSON))

	// 1. Standard Base64 bd-ticket-guard-server-data + integer result code.
	srv := ParseServerResponse(map[string]string{
		"Bd-Ticket-Guard-Result":      "0",
		"Bd-Ticket-Guard-Server-Data": encodedHeader,
	}, nil)
	if !srv.HasResultHeader || !srv.HasResultCode || srv.ResultCode != 0 || !srv.HasServerData {
		t.Fatalf("unexpected parsed result metadata: %+v", srv)
	}
	if srv.TsSign != "ts.2.synth_srv_ts_01" || srv.Ticket != "hash.SYNTH_SRV_TICKET_01" || srv.ClientCert != "SYNTH_CERT_01" {
		t.Fatalf("unexpected parsed server-data fields: %+v", srv)
	}

	// 2. URL-encoded Base64 tt-ticket-guard-server-data + negative result code.
	urlEncHeader := url.QueryEscape(base64.StdEncoding.EncodeToString([]byte(`{"ts_sign_ree":"ts.2.synth_ree_02"}`)))
	srv2 := ParseServerResponse(map[string]string{
		"bd-ticket-guard-result":      "-1",
		"tt-ticket-guard-server-data": urlEncHeader,
		"tt-ticket-guard-ticket":      "hash.SYNTH_DIRECT_TICKET_02",
	}, nil)
	if !srv2.HasResultCode || srv2.ResultCode != -1 || srv2.TsSign != "ts.2.synth_ree_02" || srv2.Ticket != "hash.SYNTH_DIRECT_TICKET_02" {
		t.Fatalf("unexpected tt-server-data parse: %+v", srv2)
	}

	// 3. Set-Cookie bd_ticket_guard_server_data fallback + JSON result header clamping.
	srv3 := ParseServerResponse(map[string]string{
		"bd-ticket-guard-result": `{"result": 9999999999999}`,
	}, map[string]string{
		"bd_ticket_guard_server_data": url.QueryEscape(encodedHeader),
	})
	if !srv3.HasResultCode || srv3.ResultCode != 2147483647 {
		t.Fatalf("expected 32-bit clamped result code, got %+v", srv3)
	}
	if srv3.TsSign != "ts.2.synth_srv_ts_01" {
		t.Fatalf("Set-Cookie fallback failed: %+v", srv3)
	}

	// 4. MergeServerData preserves existing fields when omitted and marks changed=false on no-op.
	base, err := GenerateState(nil, func() time.Time { return time.Unix(synthVectorTimestamp, 0).UTC() })
	if err != nil {
		t.Fatal(err)
	}
	merged, changed := MergeServerData(base, srv, func() time.Time { return time.Unix(synthVectorTimestamp+10, 0).UTC() })
	if !changed || merged.TsSign != srv.TsSign || merged.Ticket != srv.Ticket || merged.ClientCert != srv.ClientCert {
		t.Fatalf("MergeServerData failed: changed=%v merged=%+v", changed, merged)
	}
	merged2, changed2 := MergeServerData(merged, ServerData{HasResultHeader: true, HasResultCode: true, ResultCode: 0}, nil)
	if changed2 || merged2.TsSign != srv.TsSign || merged2.Ticket != srv.Ticket {
		t.Fatalf("MergeServerData no-op mutated state: changed=%v merged=%+v", changed2, merged2)
	}
}

func TestIsGuardedPath(t *testing.T) {
	for _, p := range []string{
		"/aweme/v1/web/user/profile/self/",
		"https://www.douyin.com/aweme/v1/web/user/profile/self?aid=6383",
		"/aweme/v1/web/aweme/favorite/",
		"https://www.douyin.com/aweme/v1/web/aweme/favorite/?max_cursor=0",
	} {
		if !IsGuardedPath(p) {
			t.Fatalf("IsGuardedPath(%q) = false, want true", p)
		}
	}
	for _, p := range []string{
		"/aweme/v1/web/aweme/post/",
		"/aweme/v1/web/aweme/detail/",
		"/aweme/v1/web/collects/list/",
		"/aweme/v1/web/history/read/",
		"",
	} {
		if IsGuardedPath(p) {
			t.Fatalf("IsGuardedPath(%q) = true, want false", p)
		}
	}
}
