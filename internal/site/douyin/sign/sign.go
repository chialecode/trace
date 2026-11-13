package sign

import (
	"context"
	"crypto/md5"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
	"unicode/utf16"
)

const (
	// QueryParamABogus is the query parameter key appended in stage 1.
	QueryParamABogus = "a_bogus"
	// QueryParamSecSDKSignature is the query/header key appended in stage 2.
	QueryParamSecSDKSignature = "x-secsdk-web-signature"
	// HeaderSecSDKExpire is the header-only expiration timestamp key for stage 2.
	HeaderSecSDKExpire = "x-secsdk-web-expire"
	// HeaderUIFID is the header key carrying the uifid token in stage 2.
	HeaderUIFID = "uifid"

	// DefaultAID is Douyin PC Web's application ID.
	DefaultAID uint32 = 6383
	// DefaultPageID is Douyin PC Web's default page ID.
	DefaultPageID uint32 = 6241
	// DefaultBrowserInfo is the default 9-field desktop viewport/platform descriptor.
	DefaultBrowserInfo = "1920|947|1920|1032|1920|1032|1920|1080|Win32"

	abogusSalt       = "dhzx"
	fortnightEpochMs = int64(1721836800000)
	fortnightSpanMs  = int64(1209600000)
	payloadStreamKey = byte(0xD3)

	alphabetUA   = "ckdp1h4ZKsUB80/Mfvw36XIgR25+WQAlEi7NLboqYTOPuzmFjJnryx9HVGDaStCe"
	alphabetWire = "Dkdpgh2ZmsQB80/MfvV36XI1R45-WUAlEixNLwoqYTOPuzKFjJnry79HbGcaStCe"

	secSDKSalt    = "A96D855A08C0A9707F8BEF0D9A527E4E"
	msTokenChars  = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+-"
	msTokenRandom = 126
)

var (
	// ErrEmptyUserAgent indicates SignInput.UserAgent was blank.
	ErrEmptyUserAgent = errors.New("douyin/sign: user-agent is required")
	// ErrClockBeforeEpoch indicates the signing clock precedes 2024-07-24T16:00:00Z.
	ErrClockBeforeEpoch = errors.New("douyin/sign: clock precedes fortnight epoch 1721836800000")
)

var uifidCookiePriority = [...]string{
	"uifid",
	"uifid_temp",
	"uifidtemp",
	"UIFID",
	"UIFID_TEMP",
	"UIFIDTEMP",
}

var protectedStage2Paths = map[string]struct{}{
	"/aweme/v1/web/aweme/detail/":         {},
	"/aweme/v1/web/aweme/post/":           {},
	"/aweme/v1/web/aweme/favorite/":       {},
	"/aweme/v1/web/aweme/listcollection/": {},
	"/aweme/v1/web/mix/aweme/":            {},
	"/aweme/v1/web/tab/feed/":             {},
	"/aweme/v1/web/mix/list/":             {},
	"/aweme/v1/web/music/aweme/":          {},
	"/aweme/v1/web/music/list/":           {},
	"/aweme/v1/web/mix/detail/":           {},
	"/aweme/v1/web/mix/listcollection/":   {},
	"/aweme/v1/web/music/detail/":         {},
	"/aweme/v1/web/collects/list/":        {},
	"/aweme/v1/web/collects/video/list/":  {},
}

// Pair represents a single ordered query parameter key-value pair.
type Pair struct {
	Key   string
	Value string
}

// SignInput holds the request context required to produce a signed query string
// and optional gateway headers without exposing HTTP or credential internals.
type SignInput struct {
	Endpoint    string
	Params      []Pair
	RawQuery    string
	Body        string
	ContentType string
	UserAgent   string
	BrowserInfo string
	PageID      uint32
	AID         uint32
	UIFID       string
	VerifyFp    string
	Now         func() time.Time
	Entropy     io.Reader
}

// SignResult contains the complete signed query string and companion headers.
type SignResult struct {
	QueryParam      string
	Signature       string
	Stage2Signature string
	SignedQuery     string
	Headers         map[string]string
}

// Signer computes Douyin request signatures.
type Signer interface {
	Sign(ctx context.Context, in SignInput) (SignResult, error)
}

// Options configures a Signer with injectable clock and entropy sources.
type Options struct {
	Now     func() time.Time
	Entropy io.Reader
}

type nativeSigner struct {
	now     func() time.Time
	entropy io.Reader
}

// NewSigner returns a clean-room Signer backed by the Go standard library.
func NewSigner(opts Options) Signer {
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	ent := opts.Entropy
	if ent == nil {
		ent = rand.Reader
	}
	return &nativeSigner{
		now:     nowFn,
		entropy: ent,
	}
}

var defaultSigner = NewSigner(Options{})

// Sign computes a Douyin request signature using the default Signer.
func Sign(ctx context.Context, in SignInput) (SignResult, error) {
	return defaultSigner.Sign(ctx, in)
}

func (s *nativeSigner) Sign(ctx context.Context, in SignInput) (SignResult, error) {
	if ctx != nil {
		if err := ctx.Err(); err != nil {
			return SignResult{}, err
		}
	}

	ua := strings.TrimSpace(in.UserAgent)
	if ua == "" {
		return SignResult{}, ErrEmptyUserAgent
	}

	nowFn := in.Now
	if nowFn == nil {
		nowFn = s.now
	}
	now := nowFn()
	nowMs := now.UnixMilli()
	if nowMs < fortnightEpochMs {
		return SignResult{}, fmt.Errorf("%w: got %d", ErrClockBeforeEpoch, nowMs)
	}

	ent := in.Entropy
	if ent == nil {
		ent = s.entropy
	}

	browserInfo := in.BrowserInfo
	if browserInfo == "" {
		browserInfo = DefaultBrowserInfo
	}
	pageID := in.PageID
	if pageID == 0 {
		pageID = DefaultPageID
	}
	aid := in.AID
	if aid == 0 {
		aid = DefaultAID
	}

	var stage1Query string
	var orderedPairs []Pair
	useRawStage1 := len(in.Params) == 0 && in.RawQuery != ""
	if useRawStage1 {
		stage1Query = in.RawQuery
	} else {
		orderedPairs = make([]Pair, len(in.Params))
		copy(orderedPairs, in.Params)
		stage1Query = SerializePairs(orderedPairs)
	}

	effectiveBody := in.Body
	if strings.Contains(strings.ToLower(in.ContentType), "multipart/form-data") {
		effectiveBody = ""
	}

	sig1, err := buildABogus(stage1Query, effectiveBody, ua, browserInfo, nowMs, pageID, aid, ent)
	if err != nil {
		return SignResult{}, err
	}

	uifid := strings.TrimSpace(in.UIFID)
	verifyFp := strings.TrimSpace(in.VerifyFp)
	headers := make(map[string]string)

	if uifid == "" || !IsProtectedPath(in.Endpoint) {
		encodedSig1 := EncodeComponent(sig1)
		var signedQ string
		if stage1Query == "" {
			signedQ = QueryParamABogus + "=" + encodedSig1
		} else {
			signedQ = stage1Query + "&" + QueryParamABogus + "=" + encodedSig1
		}
		return SignResult{
			QueryParam:  QueryParamABogus,
			Signature:   sig1,
			SignedQuery: signedQ,
			Headers:     headers,
		}, nil
	}

	// Stage 2: ArgusSecurityPlugin query & header signing.
	if useRawStage1 {
		orderedPairs, err = parseRawQueryOrder(in.RawQuery)
		if err != nil {
			return SignResult{}, err
		}
	}
	orderedPairs = append(orderedPairs, Pair{Key: QueryParamABogus, Value: sig1})

	if verifyFp != "" {
		if !containsPairKey(orderedPairs, "verifyFp") {
			orderedPairs = append(orderedPairs, Pair{Key: "verifyFp", Value: verifyFp})
		}
		if !containsPairKey(orderedPairs, "fp") {
			orderedPairs = append(orderedPairs, Pair{Key: "fp", Value: verifyFp})
		}
	}
	if !containsPairKey(orderedPairs, HeaderUIFID) {
		orderedPairs = append(orderedPairs, Pair{Key: HeaderUIFID, Value: uifid})
	}

	stamp := strconv.FormatInt(nowMs/1000, 10)
	setOrAppendPair(&orderedPairs, "timestamp", stamp)

	stage2Query := SerializePairs(orderedPairs)
	preimage := uifid + "_" + stamp + "_" + secSDKSalt + "_" + stage2Query
	md5Digest := md5.Sum([]byte(preimage))
	secSig := hex.EncodeToString(md5Digest[:])

	headers[HeaderUIFID] = uifid
	headers[QueryParamSecSDKSignature] = secSig
	headers[HeaderSecSDKExpire] = stamp

	return SignResult{
		QueryParam:      QueryParamABogus,
		Signature:       sig1,
		Stage2Signature: secSig,
		SignedQuery:     stage2Query + "&" + QueryParamSecSDKSignature + "=" + secSig,
		Headers:         headers,
	}, nil
}

// ExtractUIFID returns the first non-empty uifid token from cookies per #sign-spec §A.7.2.
func ExtractUIFID(cookies map[string]string) string {
	for _, key := range uifidCookiePriority {
		if val := strings.TrimSpace(cookies[key]); val != "" {
			return val
		}
	}
	return ""
}

// ExtractVerifyFp returns the non-empty s_v_web_id Cookie value when present.
func ExtractVerifyFp(cookies map[string]string) string {
	return strings.TrimSpace(cookies["s_v_web_id"])
}

// IsProtectedPath reports whether endpoint is among the 14 stage-2 protected paths.
func IsProtectedPath(endpoint string) bool {
	norm := normalizeEndpointPath(endpoint)
	_, ok := protectedStage2Paths[norm]
	return ok
}

func normalizeEndpointPath(endpoint string) string {
	p := strings.TrimSpace(endpoint)
	if p == "" {
		return ""
	}
	if idx := strings.Index(p, "://"); idx >= 0 {
		rest := p[idx+3:]
		if slash := strings.IndexByte(rest, '/'); slash >= 0 {
			p = rest[slash:]
		} else {
			return "/"
		}
	}
	if q := strings.IndexByte(p, '?'); q >= 0 {
		p = p[:q]
	}
	if h := strings.IndexByte(p, '#'); h >= 0 {
		p = p[:h]
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	if len(p) > 1 && !strings.HasSuffix(p, "/") {
		p += "/"
	}
	return p
}

// EncodeComponent percent-encodes a single query component leaving ASCII
// alphanumerics and '-', '_', '.', '*' unescaped (#sign-spec §A.7.3).
func EncodeComponent(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	const hexUpper = "0123456789ABCDEF"
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isSafeQueryByte(c) {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(hexUpper[c>>4])
			b.WriteByte(hexUpper[c&0x0F])
		}
	}
	return b.String()
}

func isSafeQueryByte(c byte) bool {
	return (c >= 'A' && c <= 'Z') ||
		(c >= 'a' && c <= 'z') ||
		(c >= '0' && c <= '9') ||
		c == '-' || c == '_' || c == '.' || c == '*'
}

// SerializePairs encodes ordered key-value pairs without sorting or mutating order.
func SerializePairs(pairs []Pair) string {
	if len(pairs) == 0 {
		return ""
	}
	var b strings.Builder
	for i, p := range pairs {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(EncodeComponent(p.Key))
		b.WriteByte('=')
		b.WriteString(EncodeComponent(p.Value))
	}
	return b.String()
}

func parseRawQueryOrder(raw string) ([]Pair, error) {
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, "&")
	out := make([]Pair, 0, len(parts))
	for _, seg := range parts {
		if seg == "" {
			continue
		}
		k, v, _ := strings.Cut(seg, "=")
		dk, err := decodeQueryComponent(k)
		if err != nil {
			return nil, err
		}
		dv, err := decodeQueryComponent(v)
		if err != nil {
			return nil, err
		}
		out = append(out, Pair{Key: dk, Value: dv})
	}
	return out, nil
}

func decodeQueryComponent(s string) (string, error) {
	var out []byte
	out = make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '+':
			out = append(out, ' ')
		case '%':
			if i+2 >= len(s) {
				return "", fmt.Errorf("douyin/sign: invalid percent escape in %q", s)
			}
			hi, ok1 := fromHexNibble(s[i+1])
			lo, ok2 := fromHexNibble(s[i+2])
			if !ok1 || !ok2 {
				return "", fmt.Errorf("douyin/sign: invalid percent hex in %q", s)
			}
			out = append(out, (hi<<4)|lo)
			i += 2
		default:
			out = append(out, s[i])
		}
	}
	return string(out), nil
}

func fromHexNibble(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	default:
		return 0, false
	}
}

func containsPairKey(pairs []Pair, key string) bool {
	for _, p := range pairs {
		if p.Key == key {
			return true
		}
	}
	return false
}

func setOrAppendPair(pairs *[]Pair, key, val string) {
	s := *pairs
	for i := range s {
		if s[i].Key == key {
			s[i].Value = val
			return
		}
	}
	*pairs = append(s, Pair{Key: key, Value: val})
}

// GenerateMSToken generates a 128-character fallback msToken (126 random chars + "==").
func GenerateMSToken(entropy io.Reader) (string, error) {
	if entropy == nil {
		entropy = rand.Reader
	}
	var raw [msTokenRandom]byte
	if _, err := io.ReadFull(entropy, raw[:]); err != nil {
		return "", fmt.Errorf("douyin/sign: read msToken entropy: %w", err)
	}
	var b strings.Builder
	b.Grow(msTokenRandom + 2)
	for _, v := range raw {
		b.WriteByte(msTokenChars[v&0x3F])
	}
	b.WriteString("==")
	return b.String(), nil
}

// GenerateWebID generates a 19-digit numeric webid starting with '7'.
func GenerateWebID(entropy io.Reader) (string, error) {
	if entropy == nil {
		entropy = rand.Reader
	}
	var raw [18]byte
	if _, err := io.ReadFull(entropy, raw[:]); err != nil {
		return "", fmt.Errorf("douyin/sign: read webid entropy: %w", err)
	}
	var b strings.Builder
	b.Grow(19)
	b.WriteByte('7')
	for _, v := range raw {
		b.WriteByte('0' + (v % 10))
	}
	return b.String(), nil
}

func buildABogus(query, body, ua, browserInfo string, nowMs int64, pageID, aid uint32, entropy io.Reader) (string, error) {
	dq := doubleSM3String(query + abogusSalt)
	db := doubleSM3String(body + abogusSalt)

	uaUnits := encodeUTF16Units(ua)
	uaCipher := transformModifiedRC4([]byte{0x00, 0x01, 0x0E}, uaUnits)
	uaEncoded := encodeCustomBase64(alphabetUA, uaCipher)
	dua := SumSM3([]byte(uaEncoded))

	infoBytes := encodeUTF16Units(browserInfo)
	tailBytes := encodeUTF16Units(strconv.FormatInt((nowMs+3)&0xFF, 10) + ",")

	scalars := packScalarBlock(nowMs, pageID, aid, len(infoBytes), len(tailBytes), dq, db, dua)
	rawBodyLen := len(scalars) + len(infoBytes) + len(tailBytes) + 1
	fullGroups := rawBodyLen / 3

	entBuf := make([]byte, 6+fullGroups)
	if _, err := io.ReadFull(entropy, entBuf); err != nil {
		return "", fmt.Errorf("douyin/sign: read entropy: %w", err)
	}

	probeRaw := int(entBuf[2]) % 240
	nProbe := byte(probeRaw)
	if probeRaw > 109 {
		nProbe = byte(probeRaw + (probeRaw % 2) + 1)
	}
	nTripwire := (entBuf[3] & 0x4D) | 0xB2

	verLow := interleavePair(1, 0, entBuf[0], entBuf[1])
	verHigh := interleavePair(1, 0, nProbe, nTripwire)
	var version [8]byte
	copy(version[0:4], verLow[:])
	copy(version[4:8], verHigh[:])

	var checksum byte
	for _, v := range version {
		checksum ^= v
	}
	for _, v := range scalars {
		checksum ^= v
	}

	famNoise := byte((browserFamilyOffset(ua) + int(entBuf[5])%40) & 0xFF)
	header := interleavePair(3, 82, entBuf[4], famNoise)

	rawBody := make([]byte, 0, rawBodyLen)
	rawBody = append(rawBody, scalars[:]...)
	rawBody = append(rawBody, infoBytes...)
	rawBody = append(rawBody, tailBytes...)
	rawBody = append(rawBody, checksum)

	expanded := expandBodyGroups(rawBody, entBuf[6:])
	inner := make([]byte, 0, len(version)+len(expanded))
	inner = append(inner, version[:]...)
	inner = append(inner, expanded...)

	sealed := transformModifiedRC4([]byte{payloadStreamKey}, inner)
	wire := make([]byte, 0, len(header)+len(sealed))
	wire = append(wire, header[:]...)
	wire = append(wire, sealed...)

	return encodeCustomBase64(alphabetWire, wire), nil
}

func doubleSM3String(s string) [32]byte {
	first := SumSM3([]byte(s))
	return SumSM3(first[:])
}

func pickCanaryByte(digest [32]byte, start int, sentinel, fallback byte) byte {
	for i := start; i < len(digest); i++ {
		if digest[i] != sentinel {
			return digest[i]
		}
	}
	return fallback
}

func packScalarBlock(nowMs int64, pageID, aid uint32, infoLen, tailLen int, dq, db, dua [32]byte) [50]byte {
	fortnights := byte(((nowMs - fortnightEpochMs) / fortnightSpanMs) & 0xFF)
	ink := uint64(nowMs - 1)
	ts := uint64(nowMs)

	q0, q1, qc := dq[9], dq[18], pickCanaryByte(dq, 11, 3, 12)
	b0, b1, bc := db[10], db[19], pickCanaryByte(db, 8, 4, 9)
	u0, u1, uc := dua[11], dua[21], pickCanaryByte(dua, 12, 5, 13)

	return [50]byte{
		byte(ts >> 40),     // 0: S_now[5]
		14,                 // 1: S_detect[0]
		u0,                 // 2: S_ua[0]
		byte(ink >> 8),     // 3: S_ink[1]
		byte(aid >> 16),    // 4: S_aid[2]
		byte(ts),           // 5: S_now[0]
		byte(pageID >> 24), // 6: S_page[3]
		0,                  // 7: S_detect[1]
		1,                  // 8: S_env[0]
		q1,                 // 9: S_query[1]
		0x21,               // 10: S_nr[0]
		3,                  // 11: S_tripLock
		qc,                 // 12: S_query[2]
		byte(pageID >> 8),  // 13: S_page[1]
		3,                  // 14: S_elapsed
		q0,                 // 15: S_query[0]
		byte(ink >> 32),    // 16: S_ink[4]
		0,                  // 17: S_detect[3]
		byte(ts >> 8),      // 18: S_now[1]
		byte(aid),          // 19: S_aid[0]
		fortnights,         // 20: S_fortnights
		bc,                 // 21: S_body[2]
		byte(ts >> 16),     // 22: S_now[2]
		byte(pageID >> 16), // 23: S_page[2]
		uc,                 // 24: S_ua[2]
		0,                  // 25: S_nrTag[0]
		byte(ink >> 16),    // 26: S_ink[2]
		byte(ink >> 24),    // 27: S_ink[3]
		6,                  // 28: S_bucket
		byte(aid >> 8),     // 29: S_aid[1]
		0,                  // 30: S_nrTag[1]
		byte(aid >> 24),    // 31: S_aid[3]
		u1,                 // 32: S_ua[1]
		b0,                 // 33: S_body[0]
		0,                  // 34: S_nrTag[2]
		0,                  // 35: S_nr[1]
		byte(ts >> 32),     // 36: S_now[4]
		byte(pageID),       // 37: S_page[0]
		b1,                 // 38: S_body[1]
		0,                  // 39: S_nrTag[3]
		byte(ink >> 40),    // 40: S_ink[5]
		0,                  // 41: S_detect[2]
		0,                  // 42: S_env[1]
		41,                 // 43: S_marker
		byte(ink),          // 44: S_ink[0]
		byte(ts >> 24),     // 45: S_now[3]
		byte(infoLen),      // 46: S_infoLen[0]
		byte(infoLen >> 8), // 47: S_infoLen[1]
		byte(tailLen),      // 48: S_tailLen[0]
		byte(tailLen >> 8), // 49: S_tailLen[1]
	}
}

func interleavePair(p0, p1, nLow, nHigh byte) [4]byte {
	return [4]byte{
		(nLow & 0xAA) | (p0 & 0x55),
		(nLow & 0x55) | (p0 & 0xAA),
		(nHigh & 0xAA) | (p1 & 0x55),
		(nHigh & 0x55) | (p1 & 0xAA),
	}
}

func expandBodyGroups(rawBody []byte, groupNoise []byte) []byte {
	out := make([]byte, 0, (len(rawBody)/3)*4+2)
	gIdx := 0
	for off := 0; off < len(rawBody); off += 3 {
		rem := rawBody[off:]
		if len(rem) >= 3 {
			n := groupNoise[gIdx]
			gIdx++
			out = append(out,
				(n&0x91)|(rem[0]&0x6E),
				(n&0x42)|(rem[1]&0xBD),
				(n&0x2C)|(rem[2]&0xD3),
				(rem[0]&0x91)|(rem[1]&0x42)|(rem[2]&0x2C),
			)
		} else {
			out = append(out, rem[0])
			if len(rem) == 2 && rem[1] != 0 {
				out = append(out, rem[1])
			}
		}
	}
	return out
}

func browserFamilyOffset(ua string) int {
	lower := strings.ToLower(ua)
	switch {
	case strings.Contains(lower, "edg"):
		return 125
	case strings.Contains(lower, "huawei"):
		return 170
	case strings.Contains(lower, "firefox"):
		return 40
	case strings.Contains(lower, "chrome"):
		return 0
	case strings.Contains(lower, "safari"):
		return 81
	default:
		return 210
	}
}

func encodeUTF16Units(s string) []byte {
	units := utf16.Encode([]rune(s))
	out := make([]byte, 0, len(units))
	for _, u := range units {
		if u <= 0x00FF {
			out = append(out, byte(u))
		} else {
			out = append(out, byte(u>>8), byte(u&0xFF))
		}
	}
	return out
}

func transformModifiedRC4(key, input []byte) []byte {
	var sbox [256]byte
	for i := 0; i < 256; i++ {
		sbox[255-i] = byte(i)
	}
	j := 0
	for i := 0; i < 256; i++ {
		j = (j*int(sbox[i]) + j + int(key[i%len(key)])) & 0xFF
		sbox[i], sbox[j] = sbox[j], sbox[i]
	}

	out := make([]byte, len(input))
	x, y := 0, 0
	for idx, b := range input {
		x = (x + 1) & 0xFF
		y = (y + int(sbox[x])) & 0xFF
		sbox[x], sbox[y] = sbox[y], sbox[x]
		out[idx] = b ^ sbox[(int(sbox[x])+int(sbox[y]))&0xFF]
	}
	return out
}

func encodeCustomBase64(alphabet string, src []byte) string {
	if len(src) == 0 {
		return ""
	}
	var b strings.Builder
	b.Grow(((len(src) + 2) / 3) * 4)
	for i := 0; i < len(src); i += 3 {
		rem := len(src) - i
		v := uint32(src[i]) << 16
		if rem > 1 {
			v |= uint32(src[i+1]) << 8
		}
		if rem > 2 {
			v |= uint32(src[i+2])
		}
		b.WriteByte(alphabet[(v>>18)&0x3F])
		b.WriteByte(alphabet[(v>>12)&0x3F])
		if rem > 1 {
			b.WriteByte(alphabet[(v>>6)&0x3F])
		} else {
			b.WriteByte('=')
		}
		if rem > 2 {
			b.WriteByte(alphabet[v&0x3F])
		} else {
			b.WriteByte('=')
		}
	}
	return b.String()
}
