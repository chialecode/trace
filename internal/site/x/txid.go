package x

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/site"
)

const (
	// TxIDEpochUnix is the x-client-transaction-id timestamp epoch (2023-05-01T07:00:00Z).
	TxIDEpochUnix int64 = 1682924400

	// TxIDKeyword is the protocol constant mixed into the Stage 2 SHA-256 digest (x.md §10.3).
	TxIDKeyword = "obfiowerehiring"

	// TxIDAdditionalByte is the trailing protocol byte appended before XOR masking (x.md §10.3).
	TxIDAdditionalByte byte = 3

	txIDFrameCount      = 4
	txIDFrameSelectByte = 5
	txIDRowModulus      = 16
	txIDMinRowValues    = 11
	txIDTotalTimeMS     = 4096.0
)

var (
	// Matches dynamic key-byte index extraction in ondemand.s.*.js per x.md §10.3.
	reOndemandKeyIndex = regexp.MustCompile(`\([A-Za-z_$][A-Za-z0-9_$]*\[\s*(\d{1,2})\s*\],\s*16\)`)
)

// TxIDMaterial holds the session-level inputs extracted from x.com/home HTML
// and abs.twimg.com ondemand.s.*.js for Stage 1 x-client-transaction-id key derivation.
type TxIDMaterial struct {
	VerificationKey string   // Base64 content from <meta name="twitter-site-verification">
	SVGPaths        []string // 4 <path d="..."> attributes from loading-x-anim-0..3
	RowIndex        int      // indices[0] parsed from ondemand.s.*.js
	KeyByteIndices  []int    // indices[1:] parsed from ondemand.s.*.js
}

// TxIDGenerator computes x-client-transaction-id header values using Stage 1 derived
// (keyBytes, animationKey) and Stage 2 per-request SHA-256 + XOR + unpadded Base64.
// It is safe for concurrent use after construction.
type TxIDGenerator struct {
	keyBytes     []byte
	animationKey string
	now          func() time.Time
	randByte     func() (byte, error)
}

// TxIDOption customizes optional runtime sources on TxIDGenerator (such as clock
// and random byte generation for deterministic golden-vector tests).
type TxIDOption func(*TxIDGenerator)

// WithTxIDClock overrides the clock used during Stage 2 transaction ID generation.
func WithTxIDClock(now func() time.Time) TxIDOption {
	return func(g *TxIDGenerator) {
		if now != nil {
			g.now = now
		}
	}
}

// WithTxIDRandByte overrides the single random XOR mask byte source used during Stage 2.
func WithTxIDRandByte(fn func() (byte, error)) TxIDOption {
	return func(g *TxIDGenerator) {
		if fn != nil {
			g.randByte = fn
		}
	}
}

// ParseOndemandIndices extracts (rowIndex, keyByteIndices) from ondemand.s.*.js
// using the x.md §10.3 index pattern.
func ParseOndemandIndices(js string) (int, []int, error) {
	matches := reOndemandKeyIndex.FindAllStringSubmatch(js, -1)
	if len(matches) < 2 {
		return 0, nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindSchemaChanged,
			Message: "X 前端脚本缺少事务头字节下标",
		}
	}
	indices := make([]int, 0, len(matches))
	for _, m := range matches {
		idx, err := strconv.Atoi(m[1])
		if err != nil || idx < 0 {
			return 0, nil, &site.Error{
				Site:    SiteID,
				Kind:    site.ErrKindSchemaChanged,
				Message: "X 前端脚本中的事务头字节下标无效",
			}
		}
		indices = append(indices, idx)
	}
	return indices[0], indices[1:], nil
}

// NewTxIDGenerator validates material, performs Stage 1 animation key derivation,
// and returns a concurrency-safe generator for Stage 2 header signing.
func NewTxIDGenerator(material TxIDMaterial, opts ...TxIDOption) (*TxIDGenerator, error) {
	keyBytes, err := decodeVerificationKey(material.VerificationKey)
	if err != nil {
		return nil, err
	}
	if len(keyBytes) <= txIDFrameSelectByte {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindSchemaChanged,
			Message: "X 页面校验密钥长度不足",
		}
	}
	if len(material.SVGPaths) != txIDFrameCount {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindSchemaChanged,
			Message: "X 页面动画 SVG 路径数量不符合预期",
		}
	}
	if material.RowIndex < 0 || material.RowIndex >= len(keyBytes) {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindSchemaChanged,
			Message: "X 事务头行下标超出校验密钥范围",
		}
	}
	if len(material.KeyByteIndices) == 0 {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindSchemaChanged,
			Message: "X 事务头缺少时间字节下标",
		}
	}
	for _, idx := range material.KeyByteIndices {
		if idx < 0 || idx >= len(keyBytes) {
			return nil, &site.Error{
				Site:    SiteID,
				Kind:    site.ErrKindSchemaChanged,
				Message: "X 事务头时间字节下标超出校验密钥范围",
			}
		}
	}

	animationKey, err := deriveAnimationKey(keyBytes, material.SVGPaths, material.RowIndex, material.KeyByteIndices)
	if err != nil {
		return nil, err
	}

	g := &TxIDGenerator{
		keyBytes:     keyBytes,
		animationKey: animationKey,
		now:          time.Now,
		randByte:     cryptoRandByte,
	}
	for _, opt := range opts {
		if opt != nil {
			opt(g)
		}
	}
	return g, nil
}

// AnimationKey returns the Stage 1 derived hexadecimal animation key.
func (g *TxIDGenerator) AnimationKey() string {
	if g == nil {
		return ""
	}
	return g.animationKey
}

// KeyBytes returns a defensive copy of the decoded verification key bytes.
func (g *TxIDGenerator) KeyBytes() []byte {
	if g == nil {
		return nil
	}
	out := make([]byte, len(g.keyBytes))
	copy(out, g.keyBytes)
	return out
}

// Generate computes the x-client-transaction-id header value for an HTTP method
// and URL path (e.g. "GET", "/i/api/graphql/<queryId>/UserTweets"). Any query
// string or fragment appended to reqPath is stripped before signing.
func (g *TxIDGenerator) Generate(method, reqPath string) (string, error) {
	if g == nil || len(g.keyBytes) == 0 || g.animationKey == "" {
		return "", &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindSchemaChanged,
			Message: "X 事务头生成器尚未初始化",
		}
	}
	normMethod := strings.ToUpper(strings.TrimSpace(method))
	if normMethod == "" {
		return "", &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindBadRequest,
			Message: "X 事务头签名缺少请求方法",
		}
	}
	normPath := strings.TrimSpace(reqPath)
	if idx := strings.IndexAny(normPath, "?#"); idx >= 0 {
		normPath = normPath[:idx]
	}
	if normPath == "" {
		return "", &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindBadRequest,
			Message: "X 事务头签名缺少请求路径",
		}
	}

	nowUnix := g.now().Unix()
	if nowUnix < TxIDEpochUnix {
		return "", &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindBadRequest,
			Message: "系统时间早于 X 事务头基准时间",
		}
	}
	seconds := uint32(nowUnix - TxIDEpochUnix)

	var timeBytes [4]byte
	binary.LittleEndian.PutUint32(timeBytes[:], seconds)

	payload := fmt.Sprintf("%s!%s!%d%s%s", normMethod, normPath, seconds, TxIDKeyword, g.animationKey)
	digest := sha256.Sum256([]byte(payload))

	plainLen := len(g.keyBytes) + len(timeBytes) + 16 + 1
	plain := make([]byte, 0, plainLen)
	plain = append(plain, g.keyBytes...)
	plain = append(plain, timeBytes[:]...)
	plain = append(plain, digest[:16]...)
	plain = append(plain, TxIDAdditionalByte)

	mask, err := g.randByte()
	if err != nil {
		return "", fmt.Errorf("x: generate txid mask byte: %w", err)
	}

	masked := make([]byte, 1+len(plain))
	masked[0] = mask
	for i, b := range plain {
		masked[1+i] = b ^ mask
	}
	return base64.RawStdEncoding.EncodeToString(masked), nil
}

func decodeVerificationKey(raw string) ([]byte, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindSchemaChanged,
			Message: "X 页面缺少 twitter-site-verification 校验密钥",
		}
	}
	if decoded, err := base64.StdEncoding.DecodeString(trimmed); err == nil && len(decoded) > 0 {
		return decoded, nil
	}
	if decoded, err := base64.RawStdEncoding.DecodeString(trimmed); err == nil && len(decoded) > 0 {
		return decoded, nil
	}
	return nil, &site.Error{
		Site:    SiteID,
		Kind:    site.ErrKindSchemaChanged,
		Message: "X 页面 twitter-site-verification 校验密钥无法解码",
	}
}

func deriveAnimationKey(keyBytes []byte, svgPaths []string, rowIndex int, keyByteIndices []int) (string, error) {
	frameIdx := int(keyBytes[txIDFrameSelectByte]) % txIDFrameCount
	rows, err := parseSVGPathRows(svgPaths[frameIdx])
	if err != nil {
		return "", err
	}

	selectedRowIdx := int(keyBytes[rowIndex]) % txIDRowModulus
	if selectedRowIdx >= len(rows) {
		return "", &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindSchemaChanged,
			Message: "X 动画 SVG 曲线行数不足",
		}
	}
	row := rows[selectedRowIdx]
	if len(row) < txIDMinRowValues {
		return "", &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindSchemaChanged,
			Message: "X 动画 SVG 曲线参数数量不足",
		}
	}

	frameProduct := 1
	for _, idx := range keyByteIndices {
		frameProduct *= int(keyBytes[idx]) % txIDRowModulus
	}
	// Browser WAAPI currentTime is quantized to 10 ms increments over a 4096 ms duration.
	quantizedMS := math.Round(float64(frameProduct)/10.0) * 10.0
	targetTime := quantizedMS / txIDTotalTimeMS

	fromColor := [3]float64{float64(row[0]), float64(row[1]), float64(row[2])}
	toColor := [3]float64{float64(row[3]), float64(row[4]), float64(row[5])}
	toRotation := scaleAnimParam(float64(row[6]), 60.0, 360.0, true)

	var curves [4]float64
	for i := 0; i < 4; i++ {
		minVal := 0.0
		if i%2 == 1 {
			minVal = -1.0
		}
		curves[i] = scaleAnimParam(float64(row[7+i]), minVal, 1.0, false)
	}

	progress := evalCubicBezier(curves[0], curves[1], curves[2], curves[3], targetTime)

	var out strings.Builder
	for i := 0; i < 3; i++ {
		c := fromColor[i]*(1.0-progress) + toColor[i]*progress
		if c < 0 {
			c = 0
		} else if c > 255 {
			c = 255
		}
		out.WriteString(strconv.FormatInt(int64(math.Round(c)), 16))
	}

	degrees := toRotation * progress
	radians := degrees * math.Pi / 180.0
	matrix := [4]float64{
		math.Cos(radians),
		-math.Sin(radians),
		math.Sin(radians),
		math.Cos(radians),
	}
	for _, m := range matrix {
		rounded := math.Round(m*100.0) / 100.0
		hexVal := floatToHex(math.Abs(rounded))
		hexVal = strings.ReplaceAll(hexVal, ".", "")
		hexVal = strings.ReplaceAll(hexVal, "-", "")
		out.WriteString(hexVal)
	}
	// Trailing translation components (0, 0) from CSS matrix(a, b, c, d, 0, 0).
	out.WriteString("00")

	return out.String(), nil
}

// parseSVGPathRows splits an SVG <path d="..."> attribute on 'C' commands after
// the leading moveto prefix and extracts non-negative integer sequences per row.
func parseSVGPathRows(pathData string) ([][]int, error) {
	firstC := strings.IndexByte(pathData, 'C')
	if firstC < 0 {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindSchemaChanged,
			Message: "X 动画 SVG 路径缺少贝塞尔曲线段",
		}
	}
	segments := strings.Split(pathData[firstC+1:], "C")
	rows := make([][]int, 0, len(segments))
	for _, seg := range segments {
		nums := extractUnsignedInts(seg)
		if len(nums) == 0 {
			return nil, &site.Error{
				Site:    SiteID,
				Kind:    site.ErrKindSchemaChanged,
				Message: "X 动画 SVG 曲线段缺少数值",
			}
		}
		rows = append(rows, nums)
	}
	if len(rows) == 0 {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindSchemaChanged,
			Message: "X 动画 SVG 路径解析结果为空",
		}
	}
	return rows, nil
}

func extractUnsignedInts(s string) []int {
	var nums []int
	i := 0
	for i < len(s) {
		if s[i] < '0' || s[i] > '9' {
			i++
			continue
		}
		val := 0
		for i < len(s) && s[i] >= '0' && s[i] <= '9' {
			val = val*10 + int(s[i]-'0')
			i++
		}
		nums = append(nums, val)
	}
	return nums
}

func scaleAnimParam(val, minVal, maxVal float64, floorResult bool) float64 {
	scaled := val*(maxVal-minVal)/255.0 + minVal
	if floorResult {
		return math.Floor(scaled)
	}
	return math.Round(scaled*100.0) / 100.0
}

// evalCubicBezier evaluates the CSS cubic-bezier(x1, y1, x2, y2) easing function at t.
func evalCubicBezier(x1, y1, x2, y2, t float64) float64 {
	if t <= 0.0 {
		startSlope := 0.0
		if x1 > 0.0 {
			startSlope = y1 / x1
		} else if y1 == 0.0 && x2 > 0.0 {
			startSlope = y2 / x2
		}
		return startSlope * t
	}
	if t >= 1.0 {
		endSlope := 0.0
		if x2 < 1.0 {
			endSlope = (y2 - 1.0) / (x2 - 1.0)
		} else if x2 == 1.0 && x1 < 1.0 {
			endSlope = (y1 - 1.0) / (x1 - 1.0)
		}
		return 1.0 + endSlope*(t-1.0)
	}

	lo, hi := 0.0, 1.0
	mid := 0.5
	for iter := 0; iter < 64 && hi-lo > 1e-9; iter++ {
		mid = (lo + hi) * 0.5
		xEst := cubicBezierAxis(x1, x2, mid)
		if math.Abs(t-xEst) < 1e-7 {
			return cubicBezierAxis(y1, y2, mid)
		}
		if xEst < t {
			lo = mid
		} else {
			hi = mid
		}
	}
	return cubicBezierAxis(y1, y2, mid)
}

func cubicBezierAxis(p1, p2, u float64) float64 {
	inv := 1.0 - u
	return 3.0*p1*inv*inv*u + 3.0*p2*inv*u*u + u*u*u
}

// floatToHex formats a non-negative IEEE-754 float64 in base-16 matching
// ECMAScript Number.prototype.toString(16) (exact terminating hex fraction).
func floatToHex(x float64) string {
	if x < 0 {
		x = -x
	}
	intPart := uint64(x)
	frac := x - float64(intPart)
	if frac == 0 {
		return strconv.FormatUint(intPart, 16)
	}
	var b strings.Builder
	b.WriteString(strconv.FormatUint(intPart, 16))
	b.WriteByte('.')
	const hexDigits = "0123456789abcdef"
	for i := 0; frac > 0 && i < 24; i++ {
		frac *= 16.0
		digit := int(frac)
		b.WriteByte(hexDigits[digit])
		frac -= float64(digit)
	}
	return b.String()
}

func cryptoRandByte() (byte, error) {
	var b [1]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, err
	}
	return b[0], nil
}
