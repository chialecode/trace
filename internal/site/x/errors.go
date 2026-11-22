package x

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/chialecode/trace/internal/site"
)

const (
	riskLockedMessage = "X 要求验证或账号被临时锁定，TRACE 已停止 X 的全部自动请求，请先在浏览器里处理"
)

// RateLimitError carries structured X rate-limit reset metadata wrapped inside a
// site.ErrKindRateLimited *site.Error (extractable via errors.As or RateLimitDetails).
type RateLimitError struct {
	Operation    string        `json:"operation,omitempty"`
	Limit        int           `json:"limit,omitempty"`
	Remaining    int           `json:"remaining"`
	ResetAt      time.Time     `json:"resetAt,omitempty"`
	BlockedUntil time.Time     `json:"blockedUntil,omitempty"`
	RetryAfter   time.Duration `json:"retryAfter"`
}

func (e *RateLimitError) Error() string {
	if e == nil {
		return ""
	}
	return FormatRateLimitMessage(e.RetryAfter)
}

// RateLimitResetAt exposes the upstream x-rate-limit-reset timestamp for
// cross-package schedulers (e.g. internal/subscription) without importing internal/site/x.
func (e *RateLimitError) RateLimitResetAt() time.Time {
	if e == nil {
		return time.Time{}
	}
	if !e.ResetAt.IsZero() {
		return e.ResetAt
	}
	return e.BlockedUntil
}

// RateLimitDetails extracts structured rate-limit metadata from err when present.
func RateLimitDetails(err error) (*RateLimitError, bool) {
	if err == nil {
		return nil, false
	}
	var rl *RateLimitError
	if errors.As(err, &rl) && rl != nil {
		return rl, true
	}
	return nil, false
}

// FormatRateLimitMessage formats the user-facing Chinese recovery time message
// mandated by x.md §15.1 #13 ("X 限流，约 N 分钟后再试").
func FormatRateLimitMessage(wait time.Duration) string {
	mins := 1
	if wait > 0 {
		m := int(math.Ceil(wait.Minutes()))
		if m > 1 {
			mins = m
		}
	}
	return fmt.Sprintf("X 限流，约 %d 分钟后再试", mins)
}

// ParsedRateLimitHeaders captures parsed x-rate-limit-* HTTP response headers.
type ParsedRateLimitHeaders struct {
	Limit        int
	HasLimit     bool
	Remaining    int
	HasRemaining bool
	ResetUnix    int64
	HasReset     bool
}

// ParseRateLimitHeaders parses X's x-rate-limit-limit, x-rate-limit-remaining,
// and x-rate-limit-reset response headers.
func ParseRateLimitHeaders(h http.Header) ParsedRateLimitHeaders {
	var out ParsedRateLimitHeaders
	if h == nil {
		return out
	}
	if raw := strings.TrimSpace(h.Get("x-rate-limit-limit")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= 0 {
			out.Limit = v
			out.HasLimit = true
		}
	}
	if raw := strings.TrimSpace(h.Get("x-rate-limit-remaining")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v >= 0 {
			out.Remaining = v
			out.HasRemaining = true
		}
	}
	if raw := strings.TrimSpace(h.Get("x-rate-limit-reset")); raw != "" {
		if v, err := strconv.ParseInt(raw, 10, 64); err == nil && v > 0 {
			out.ResetUnix = v
			out.HasReset = true
		}
	}
	return out
}

// XAPIErrorItem represents one entry inside X's REST or GraphQL errors[] payload.
type XAPIErrorItem struct {
	Code    int
	HasCode bool
	Message string
	Name    string
	Kind    string
}

// ExtractXErrors decodes top-level errors[] from an X REST or GraphQL JSON payload
// using json.Decoder.UseNumber() so large IDs never touch float64.
func ExtractXErrors(body []byte) []XAPIErrorItem {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil
	}
	var env struct {
		Errors []struct {
			Code       json.RawMessage `json:"code"`
			Message    string          `json:"message"`
			Name       string          `json:"name"`
			Kind       string          `json:"kind"`
			Extensions *struct {
				Code json.RawMessage `json:"code"`
				Name string          `json:"name"`
				Kind string          `json:"kind"`
			} `json:"extensions"`
		} `json:"errors"`
	}
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&env); err != nil || len(env.Errors) == 0 {
		return nil
	}
	out := make([]XAPIErrorItem, 0, len(env.Errors))
	for _, item := range env.Errors {
		var parsed XAPIErrorItem
		parsed.Message = strings.TrimSpace(item.Message)
		parsed.Name = strings.TrimSpace(item.Name)
		parsed.Kind = strings.TrimSpace(item.Kind)
		if v, ok := parseStrictJSONInt(item.Code); ok {
			parsed.Code = v
			parsed.HasCode = true
		} else if item.Extensions != nil {
			if v, ok := parseStrictJSONInt(item.Extensions.Code); ok {
				parsed.Code = v
				parsed.HasCode = true
			}
		}
		if item.Extensions != nil {
			if parsed.Name == "" {
				parsed.Name = strings.TrimSpace(item.Extensions.Name)
			}
			if parsed.Kind == "" {
				parsed.Kind = strings.TrimSpace(item.Extensions.Kind)
			}
		}
		out = append(out, parsed)
	}
	return out
}

// HasErrorCode reports whether any item in errs carries the specified integer X error code.
func HasErrorCode(errs []XAPIErrorItem, code int) bool {
	for _, e := range errs {
		if e.HasCode && e.Code == code {
			return true
		}
	}
	return false
}

// EnvelopeErrorContext holds raw HTTP response attributes inspected prior to data decoding.
type EnvelopeErrorContext struct {
	Endpoint                string
	Operation               string
	StatusCode              int
	Header                  http.Header
	Body                    []byte
	HasCookie               bool
	Now                     time.Time
	ConsecutiveHeaderless88 int
}

// ClassifyHTTPAndEnvelopeError inspects rate-limit headers, HTTP status codes, redirects,
// empty bodies, HTML login/challenge/Cloudflare Turnstile pages, JSON errors[], and fake
// empty shells strictly following docs/research/x.md §10.1 and §7.3 before any data parsing.
func ClassifyHTTPAndEnvelopeError(in EnvelopeErrorContext) *site.Error {
	endpoint := site.SanitizeEndpoint(in.Endpoint)
	trimmed := bytes.TrimSpace(in.Body)
	now := in.Now
	if now.IsZero() {
		now = time.Now().UTC()
	}
	rlHeaders := ParseRateLimitHeaders(in.Header)

	// 1. Redirect responses (auto-redirect is disabled on all API calls).
	if in.StatusCode >= 300 && in.StatusCode < 400 {
		return classifyXRedirectError(endpoint, in.StatusCode, in.Header.Get("Location"), in.HasCookie)
	}

	// 2. Empty-body 404 / 410 (queryId / txid drift or silent lock per x.md §10.1).
	if (in.StatusCode == http.StatusNotFound || in.StatusCode == http.StatusGone) && len(trimmed) == 0 {
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindSchemaChanged,
			HTTPStatus: in.StatusCode,
			Endpoint:   endpoint,
			Message:    fmt.Sprintf("X 接口返回空体 HTTP %d，前端接口或事务头可能已变化，已暂停 X 请求", in.StatusCode),
		}
	}

	// 3. HTTP 414 URI Too Long (oversized GraphQL features parameter per x.md §5.1).
	if in.StatusCode == http.StatusRequestURITooLong {
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindSchemaChanged,
			HTTPStatus: in.StatusCode,
			Endpoint:   endpoint,
			Message:    "X GraphQL 请求地址过长（HTTP 414），前端参数结构可能已变化",
		}
	}

	// 4. Inspect JSON errors[] across ALL HTTP statuses (400/401/403/404/429/200/5xx).
	var jsonErrors []XAPIErrorItem
	if len(trimmed) > 0 && trimmed[0] == '{' {
		jsonErrors = ExtractXErrors(trimmed)
	}
	if len(jsonErrors) > 0 {
		if se := classifyXErrorItems(endpoint, in.Operation, in.StatusCode, rlHeaders, now, in.ConsecutiveHeaderless88, jsonErrors); se != nil {
			return se
		}
	}

	// 5. Non-200 HTTP status fallbacks (when no recognized errors[] item was present).
	switch {
	case in.StatusCode == http.StatusUnauthorized:
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindAuthRequired,
			HTTPStatus: in.StatusCode,
			Endpoint:   endpoint,
			Message:    "X 登录状态已失效（HTTP 401），请重新粘贴 Cookie 登录",
		}

	case in.StatusCode == http.StatusTooManyRequests:
		// x.md §10.1 rows 8 & 9: HTTP 429 with remaining > 0 is account-level risk_control;
		// remaining == 0 (or standard first 429) is rate_limited with recovery time.
		if rlHeaders.HasRemaining && rlHeaders.Remaining > 0 {
			return &site.Error{
				Site:       SiteID,
				Kind:       site.ErrKindRiskControl,
				HTTPStatus: in.StatusCode,
				Endpoint:   endpoint,
				Message:    riskLockedMessage,
			}
		}
		if !rlHeaders.HasRemaining && in.ConsecutiveHeaderless88 >= 2 {
			return &site.Error{
				Site:       SiteID,
				Kind:       site.ErrKindRiskControl,
				HTTPStatus: in.StatusCode,
				Endpoint:   endpoint,
				Message:    riskLockedMessage,
			}
		}
		return buildRateLimitedError(endpoint, in.Operation, in.StatusCode, 0, rlHeaders, now)

	case in.StatusCode == http.StatusForbidden:
		if len(trimmed) > 0 && isXHTMLOrNonJSON(in.Header, trimmed) {
			return classifyXNonJSONBody(endpoint, in.StatusCode, trimmed, in.HasCookie)
		}
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindRiskControl,
			HTTPStatus: in.StatusCode,
			Endpoint:   endpoint,
			Message:    "X 拒绝访问（HTTP 403），疑似触发风控或验证，已停止 X 请求",
		}

	case in.StatusCode == http.StatusBadRequest:
		if _, drift := ShouldTriggerFrontendRefresh(in.StatusCode, trimmed); drift {
			return &site.Error{
				Site:       SiteID,
				Kind:       site.ErrKindSchemaChanged,
				HTTPStatus: in.StatusCode,
				Endpoint:   endpoint,
				Message:    "X GraphQL 前端参数已变化（HTTP 400），已暂停 X 请求",
			}
		}
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindBadRequest,
			HTTPStatus: in.StatusCode,
			Endpoint:   endpoint,
			Message:    "X 接口请求参数无效（HTTP 400）",
		}

	case in.StatusCode == http.StatusNotFound || in.StatusCode == http.StatusGone:
		if len(trimmed) > 0 && isXHTMLOrNonJSON(in.Header, trimmed) {
			return classifyXNonJSONBody(endpoint, in.StatusCode, trimmed, in.HasCookie)
		}
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindUnavailable,
			HTTPStatus: in.StatusCode,
			Endpoint:   endpoint,
			Message:    fmt.Sprintf("请求的 X 内容或用户不存在（HTTP %d）", in.StatusCode),
		}

	case in.StatusCode >= 500:
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindNetwork,
			HTTPStatus: in.StatusCode,
			Endpoint:   endpoint,
			Message:    fmt.Sprintf("X 服务端暂时不可用（HTTP %d）", in.StatusCode),
		}

	case in.StatusCode != http.StatusOK:
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindNetwork,
			HTTPStatus: in.StatusCode,
			Endpoint:   endpoint,
			Message:    fmt.Sprintf("X 接口返回异常状态码 HTTP %d", in.StatusCode),
		}
	}

	// 6. HTTP 200 body pre-parse inspection: empty body, HTML login/challenge/Cloudflare Turnstile.
	if len(trimmed) == 0 {
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindRiskControl,
			HTTPStatus: in.StatusCode,
			Endpoint:   endpoint,
			Message:    "X 接口返回空响应，疑似触发风控拦截，已停止 X 请求",
		}
	}

	if isXHTMLOrNonJSON(in.Header, trimmed) {
		return classifyXNonJSONBody(endpoint, in.StatusCode, trimmed, in.HasCookie)
	}

	// 7. JSON syntax verification + fake empty shell check ({"data":{"user":{}}}).
	var top map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&top); err != nil {
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindSchemaChanged,
			HTTPStatus: in.StatusCode,
			Endpoint:   endpoint,
			Message:    "X 接口返回的 JSON 无法解析",
			Err:        err,
		}
	}

	if isFakeEmptyUserShell(top["data"]) {
		return buildRateLimitedError(endpoint, in.Operation, in.StatusCode, 0, rlHeaders, now)
	}

	return nil
}

func classifyXErrorItems(
	endpoint, operation string,
	statusCode int,
	rlHeaders ParsedRateLimitHeaders,
	now time.Time,
	consecutiveHeaderless88 int,
	errs []XAPIErrorItem,
) *site.Error {
	// Priority 1: CSRF mismatch 353 (handled by Client for at-most-once ct0 replay before auth_required).
	if HasErrorCode(errs, 353) {
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindAuthRequired,
			Code:       353,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "X CSRF 校验令牌（ct0）不匹配，请重新粘贴 Cookie 登录",
		}
	}

	// Priority 2: Account-level suspension / lock / automation risk codes (checked BEFORE code 37!).
	if HasErrorCode(errs, 64) {
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindRiskControl,
			Code:       64,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "当前 X 账号已被封禁（code 64），TRACE 已停止 X 的全部自动请求，请在浏览器中核查账号状态",
		}
	}
	if HasErrorCode(errs, 326) {
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindRiskControl,
			Code:       326,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    riskLockedMessage,
		}
	}
	if HasErrorCode(errs, 226) {
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindRiskControl,
			Code:       226,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    riskLockedMessage,
		}
	}

	// Priority 3: Authentication failure codes (32, 89, 215, 239).
	for _, authCode := range []int{32, 89, 215, 239} {
		if HasErrorCode(errs, authCode) {
			return &site.Error{
				Site:       SiteID,
				Kind:       site.ErrKindAuthRequired,
				Code:       authCode,
				HTTPStatus: statusCode,
				Endpoint:   endpoint,
				Message:    fmt.Sprintf("X 登录状态已失效（code %d），请重新粘贴 Cookie 登录", authCode),
			}
		}
	}

	// Priority 4: Rate-limit code 88 (x.md §10.1 rows 8 & 9).
	if HasErrorCode(errs, 88) {
		// remaining > 0 OR HTTP 200 without remaining==0 OR consecutive headerless 88 => risk_control!
		if rlHeaders.HasRemaining && rlHeaders.Remaining > 0 {
			return &site.Error{
				Site:       SiteID,
				Kind:       site.ErrKindRiskControl,
				Code:       88,
				HTTPStatus: statusCode,
				Endpoint:   endpoint,
				Message:    riskLockedMessage,
			}
		}
		if !rlHeaders.HasRemaining && (statusCode == http.StatusOK || consecutiveHeaderless88 >= 2) {
			return &site.Error{
				Site:       SiteID,
				Kind:       site.ErrKindRiskControl,
				Code:       88,
				HTTPStatus: statusCode,
				Endpoint:   endpoint,
				Message:    riskLockedMessage,
			}
		}
		return buildRateLimitedError(endpoint, operation, statusCode, 88, rlHeaders, now)
	}

	// Priority 5: GraphQL schema drift messages (features cannot be null / Query: Unspecified).
	for _, item := range errs {
		lowerMsg := strings.ToLower(item.Message)
		if strings.Contains(lowerMsg, "features cannot be null") || strings.Contains(lowerMsg, "query: unspecified") {
			return &site.Error{
				Site:       SiteID,
				Kind:       site.ErrKindSchemaChanged,
				Code:       item.Code,
				HTTPStatus: statusCode,
				Endpoint:   endpoint,
				Message:    "X GraphQL 前端接口参数已变化，已暂停 X 请求",
			}
		}
	}

	// Priority 6: Invalid variable / cursor (code 214).
	if HasErrorCode(errs, 214) {
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindBadRequest,
			Code:       214,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "X 接口请求变量或游标参数无效（code 214）",
		}
	}

	// Priority 7: Single-target forbidden / unavailable codes (NEVER trip site-wide circuit breaker).
	if HasErrorCode(errs, 37) {
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindForbidden,
			Code:       37,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "目标 X 内容或账号无权查看（code 37）",
		}
	}
	if HasErrorCode(errs, 63) {
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindUnavailable,
			Code:       63,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "目标 X 账号已被封禁（code 63）",
		}
	}
	for _, forbCode := range []int{136, 161, 179, 219} {
		if HasErrorCode(errs, forbCode) {
			return &site.Error{
				Site:       SiteID,
				Kind:       site.ErrKindForbidden,
				Code:       forbCode,
				HTTPStatus: statusCode,
				Endpoint:   endpoint,
				Message:    fmt.Sprintf("没有查看该 X 内容的权限（code %d）", forbCode),
			}
		}
	}
	for _, unavailCode := range []int{34, 50, 144} {
		if HasErrorCode(errs, unavailCode) {
			return &site.Error{
				Site:       SiteID,
				Kind:       site.ErrKindUnavailable,
				Code:       unavailCode,
				HTTPStatus: statusCode,
				Endpoint:   endpoint,
				Message:    fmt.Sprintf("目标 X 用户或帖子不存在或已删除（code %d）", unavailCode),
			}
		}
	}

	// Priority 8: Server overload / internal error (131, -1, LoadShed).
	for _, item := range errs {
		if item.HasCode && (item.Code == 131 || item.Code == -1) {
			return &site.Error{
				Site:       SiteID,
				Kind:       site.ErrKindNetwork,
				Code:       item.Code,
				HTTPStatus: statusCode,
				Endpoint:   endpoint,
				Message:    fmt.Sprintf("X 服务端暂时过载或内部故障（code %d）", item.Code),
			}
		}
		lowerName := strings.ToLower(item.Name + " " + item.Kind + " " + item.Message)
		if containsAny(lowerName, "loadshed", "over capacity", "internal error", "timeout", "service unavailable") {
			return &site.Error{
				Site:       SiteID,
				Kind:       site.ErrKindNetwork,
				Code:       item.Code,
				HTTPStatus: statusCode,
				Endpoint:   endpoint,
				Message:    "X 服务端暂时不可用，请稍后重试",
			}
		}
	}

	// Priority 9: Message-based fallback across remaining GraphQL errors.
	first := errs[0]
	lowerMsg := strings.ToLower(first.Message + " " + first.Name + " " + first.Kind)
	switch {
	case containsAny(lowerMsg, "arkose", "captcha", "challenge", "temporarily locked", "automated", "suspended", "turnstile"):
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindRiskControl,
			Code:       first.Code,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    riskLockedMessage,
		}
	case containsAny(lowerMsg, "rate limit", "too many requests"):
		return buildRateLimitedError(endpoint, operation, statusCode, first.Code, rlHeaders, now)
	case containsAny(lowerMsg, "could not authenticate", "invalid or expired token", "not authorized to access", "login required", "unauthenticated", "csrf"):
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindAuthRequired,
			Code:       first.Code,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "X 登录状态已失效，请重新粘贴 Cookie 登录",
		}
	case containsAny(lowerMsg, "protected", "blocked", "not permitted", "forbidden"):
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindForbidden,
			Code:       first.Code,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "没有查看该 X 内容的权限",
		}
	case containsAny(lowerMsg, "not found", "does not exist", "deleted", "no status found"):
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindUnavailable,
			Code:       first.Code,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "目标 X 用户或帖子不存在或已删除",
		}
	}

	if statusCode == http.StatusUnauthorized {
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindAuthRequired,
			Code:       first.Code,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "X 登录状态已失效，请重新粘贴 Cookie 登录",
		}
	}
	if statusCode == http.StatusForbidden {
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindRiskControl,
			Code:       first.Code,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    riskLockedMessage,
		}
	}
	if statusCode == http.StatusBadRequest {
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindBadRequest,
			Code:       first.Code,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "X 接口请求参数无效",
		}
	}
	return &site.Error{
		Site:       SiteID,
		Kind:       site.ErrKindUnavailable,
		Code:       first.Code,
		HTTPStatus: statusCode,
		Endpoint:   endpoint,
		Message:    "X 接口返回业务错误，暂无法完成请求",
	}
}

func buildRateLimitedError(
	endpoint, operation string,
	statusCode, code int,
	rlHeaders ParsedRateLimitHeaders,
	now time.Time,
) *site.Error {
	var resetAt time.Time
	wait := 15 * time.Minute
	if rlHeaders.HasReset {
		resetAt = time.Unix(rlHeaders.ResetUnix, 0).UTC()
		if diff := resetAt.Sub(now); diff > 0 {
			wait = diff
		} else {
			wait = time.Minute
		}
	} else {
		resetAt = now.Add(wait).UTC()
	}
	rlErr := &RateLimitError{
		Operation:    operation,
		Limit:        rlHeaders.Limit,
		Remaining:    rlHeaders.Remaining,
		ResetAt:      resetAt,
		BlockedUntil: resetAt,
		RetryAfter:   wait,
	}
	return &site.Error{
		Site:       SiteID,
		Kind:       site.ErrKindRateLimited,
		Code:       code,
		HTTPStatus: statusCode,
		Endpoint:   endpoint,
		Message:    FormatRateLimitMessage(wait),
		Err:        rlErr,
	}
}

func classifyXRedirectError(endpoint string, statusCode int, rawLocation string, hasCookie bool) *site.Error {
	loc := strings.ToLower(strings.TrimSpace(rawLocation))
	if containsAny(loc, "/account/access", "arkose", "captcha", "challenge", "funcaptcha", "/i/flow/consent", "turnstile") {
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindRiskControl,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    riskLockedMessage,
		}
	}
	if containsAny(loc, "/i/flow/login", "/login", "/account/login", "/sessions", "redirect_after_login") {
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindAuthRequired,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "X 登录状态已失效（跳转至登录页），请重新粘贴 Cookie 登录",
		}
	}
	if !hasCookie {
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindAuthRequired,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "X 接口要求登录后访问",
		}
	}
	return &site.Error{
		Site:       SiteID,
		Kind:       site.ErrKindRiskControl,
		HTTPStatus: statusCode,
		Endpoint:   endpoint,
		Message:    fmt.Sprintf("X 接口返回异常跳转（HTTP %d），已停止 X 请求", statusCode),
	}
}

func isXHTMLOrNonJSON(h http.Header, trimmed []byte) bool {
	if len(trimmed) == 0 {
		return false
	}
	if trimmed[0] != '{' && trimmed[0] != '[' {
		return true
	}
	if h != nil {
		ct := strings.ToLower(strings.TrimSpace(h.Get("Content-Type")))
		if strings.HasPrefix(ct, "text/html") {
			return true
		}
	}
	return false
}

func classifyXNonJSONBody(endpoint string, statusCode int, body []byte, hasCookie bool) *site.Error {
	snippet := body
	if len(snippet) > 16384 {
		snippet = snippet[:16384]
	}
	lower := strings.ToLower(string(snippet))

	if containsAny(lower,
		"cf-turnstile",
		"challenges.cloudflare.com",
		"challenge-platform",
		"just a moment",
		"checking your browser",
		"arkoselabs",
		"funcaptcha",
		"fc-token",
		"captcha",
		"/account/access",
		"account has been locked",
		"temporarily locked",
		"unusual login activity",
	) {
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindRiskControl,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    riskLockedMessage,
		}
	}

	if containsAny(lower,
		"/i/flow/login",
		"sign in to x",
		"log in to x",
		"log in to twitter",
		"sign in to twitter",
		"login_redirect",
		"js-login-form",
		"sessions",
	) {
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindAuthRequired,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "X 返回了登录页面，登录状态已失效，请重新粘贴 Cookie 登录",
		}
	}

	if !hasCookie {
		return &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindAuthRequired,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "X 返回了非 JSON 页面，当前未登录 X",
		}
	}

	return &site.Error{
		Site:       SiteID,
		Kind:       site.ErrKindRiskControl,
		HTTPStatus: statusCode,
		Endpoint:   endpoint,
		Message:    "X 返回了非 JSON 页面，疑似触发安全验证或拦截，已停止 X 请求",
	}
}

// isFakeEmptyUserShell detects X's signature rate-limit / risk-control fake empty shell
// {"data":{"user":{}}} (user object present with 0 keys, NOT {"user":null}).
func isFakeEmptyUserShell(rawData json.RawMessage) bool {
	trimmed := bytes.TrimSpace(rawData)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return false
	}
	var dataMap map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&dataMap); err != nil {
		return false
	}
	rawUser, hasUser := dataMap["user"]
	if !hasUser {
		return false
	}
	userTrim := bytes.TrimSpace(rawUser)
	if len(userTrim) == 0 || userTrim[0] != '{' {
		return false
	}
	var userMap map[string]json.RawMessage
	uDec := json.NewDecoder(bytes.NewReader(userTrim))
	uDec.UseNumber()
	if err := uDec.Decode(&userMap); err != nil {
		return false
	}
	if len(userMap) == 0 {
		return true
	}
	if rawRes, hasRes := userMap["result"]; hasRes {
		resTrim := bytes.TrimSpace(rawRes)
		if len(resTrim) > 0 && resTrim[0] == '{' {
			var resMap map[string]json.RawMessage
			rDec := json.NewDecoder(bytes.NewReader(resTrim))
			rDec.UseNumber()
			if rDec.Decode(&resMap) == nil && len(resMap) == 0 {
				return true
			}
		}
	}
	return false
}

// ---------- Safe JSON / String helpers (always UseNumber, never float64 for IDs) ----------

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if sub != "" && strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	return ""
}

func isJSONNull(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}

func decodeJSONUseNumber(raw []byte, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	return dec.Decode(dst)
}

func parseStrictJSONInt(raw json.RawMessage) (int, bool) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return 0, false
	}
	var n json.Number
	dec := json.NewDecoder(bytes.NewReader(t))
	dec.UseNumber()
	if err := dec.Decode(&n); err == nil {
		if v, iErr := n.Int64(); iErr == nil {
			return int(v), true
		}
	}
	var s string
	if json.Unmarshal(t, &s) == nil {
		s = strings.TrimSpace(s)
		if v, err := strconv.Atoi(s); err == nil {
			return v, true
		}
	}
	return 0, false
}

func parseJSONString(raw json.RawMessage) string {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return ""
	}
	var s string
	if json.Unmarshal(t, &s) == nil {
		return strings.TrimSpace(s)
	}
	return ""
}

func rawNumberOrString(raw json.RawMessage) string {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return ""
	}
	if t[0] == '"' {
		var s string
		if json.Unmarshal(t, &s) == nil {
			return strings.TrimSpace(s)
		}
		return ""
	}
	var n json.Number
	dec := json.NewDecoder(bytes.NewReader(t))
	dec.UseNumber()
	if err := dec.Decode(&n); err == nil {
		return strings.TrimSpace(n.String())
	}
	return ""
}

func parseJSONInt64(raw json.RawMessage) int64 {
	s := rawNumberOrString(raw)
	if s == "" {
		return 0
	}
	if v, err := strconv.ParseInt(s, 10, 64); err == nil {
		return v
	}
	return 0
}

func parseJSONBool(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || bytes.Equal(t, []byte("null")) {
		return false
	}
	var b bool
	if json.Unmarshal(t, &b) == nil {
		return b
	}
	if v, ok := parseStrictJSONInt(t); ok {
		return v != 0
	}
	return false
}
