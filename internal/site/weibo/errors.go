package weibo

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/chialecode/trace/internal/site"
)

// EnvelopeErrorContext holds raw HTTP response attributes inspected prior to JSON parsing.
type EnvelopeErrorContext struct {
	Endpoint   string
	StatusCode int
	Header     http.Header
	Body       []byte
	HasCookie  bool
	EmptyMeans string
}

// ClassifyHTTPAndEnvelopeError inspects HTTP status codes, redirects, 0-byte bodies, HTML fake-200
// pages (Sina Visitor System / Geetest / slider / login), and JSON envelopes (`ok` / `code`)
// strictly following docs/research/weibo.md §9.1 and §9.2.
func ClassifyHTTPAndEnvelopeError(in EnvelopeErrorContext) *site.Error {
	endpoint := site.SanitizeEndpoint(in.Endpoint)
	trimmed := bytes.TrimSpace(in.Body)

	// 1. Pre-parse HTTP status classification.
	switch {
	case in.StatusCode >= 300 && in.StatusCode < 400:
		return classifyRedirectError(endpoint, in.StatusCode, in.Header.Get("Location"), in.HasCookie)

	case in.StatusCode == http.StatusUnauthorized:
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindAuthRequired,
			HTTPStatus: in.StatusCode,
			Endpoint:   endpoint,
			Message:    "微博登录状态已失效，请重新粘贴 Cookie 登录",
		}

	case in.StatusCode == http.StatusRequestURITooLong || in.StatusCode == http.StatusTooManyRequests:
		// HTTP 414 is Weibo's gateway frequency-limit status; HTTP 429 is standard rate limit.
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindRateLimited,
			HTTPStatus: in.StatusCode,
			Endpoint:   endpoint,
			Message:    fmt.Sprintf("微博请求过于频繁（HTTP %d），已停止请求并进入退避", in.StatusCode),
		}

	case in.StatusCode == http.StatusTeapot || in.StatusCode == 432:
		// HTTP 418 (anti-crawl/fingerprint) and HTTP 432 (Weibo risk control / Geetest).
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindRiskControl,
			HTTPStatus: in.StatusCode,
			Endpoint:   endpoint,
			Message:    fmt.Sprintf("微博触发安全验证或风控拦截（HTTP %d），已立即停止请求", in.StatusCode),
		}

	case in.StatusCode == http.StatusForbidden:
		// Inspect explicit business error codes or visibility/unavailable messages in JSON
		// before falling back to WAF risk control (DEC-089).
		if len(trimmed) > 0 && trimmed[0] == '{' {
			if bizErr := tryClassifyExplicitForbiddenJSON(endpoint, in.StatusCode, trimmed); bizErr != nil {
				return bizErr
			}
		}
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindRiskControl,
			HTTPStatus: in.StatusCode,
			Endpoint:   endpoint,
			Message:    "微博访问被拦截（HTTP 403），已立即停止请求",
		}

	case in.StatusCode == http.StatusBadRequest:
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindBadRequest,
			HTTPStatus: in.StatusCode,
			Endpoint:   endpoint,
			Message:    "微博接口请求参数无效（HTTP 400）",
		}

	case in.StatusCode == http.StatusNotFound || in.StatusCode == http.StatusGone:
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindUnavailable,
			HTTPStatus: in.StatusCode,
			Endpoint:   endpoint,
			Message:    fmt.Sprintf("请求的微博内容或接口不可用（HTTP %d）", in.StatusCode),
		}

	case in.StatusCode >= 500:
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindNetwork,
			HTTPStatus: in.StatusCode,
			Endpoint:   endpoint,
			Message:    fmt.Sprintf("微博服务端暂时不可用（HTTP %d）", in.StatusCode),
		}

	case in.StatusCode != http.StatusOK:
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindNetwork,
			HTTPStatus: in.StatusCode,
			Endpoint:   endpoint,
			Message:    fmt.Sprintf("微博接口返回异常状态码 HTTP %d", in.StatusCode),
		}
	}

	// 2. HTTP 200 body pre-parse inspection: 0-byte body & HTML fake-200 pages.
	if len(trimmed) == 0 {
		if in.EmptyMeans != "" {
			return &site.Error{
				Site:       site.Weibo,
				Kind:       site.ErrKindUnavailable,
				HTTPStatus: in.StatusCode,
				Endpoint:   endpoint,
				Message:    in.EmptyMeans,
			}
		}
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindRiskControl,
			HTTPStatus: in.StatusCode,
			Endpoint:   endpoint,
			Message:    "微博返回空响应，疑似触发风控拦截，已停止请求",
		}
	}

	ct := strings.ToLower(strings.TrimSpace(in.Header.Get("Content-Type")))
	if strings.HasPrefix(ct, "text/html") || (trimmed[0] != '{' && trimmed[0] != '[') {
		return classifyNonJSONFake200(endpoint, in.StatusCode, trimmed, in.HasCookie)
	}

	// 3. JSON envelope inspection using UseNumber to preserve 64-bit integer precision.
	var top map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(trimmed))
	dec.UseNumber()
	if err := dec.Decode(&top); err != nil {
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindSchemaChanged,
			HTTPStatus: in.StatusCode,
			Endpoint:   endpoint,
			Message:    "微博返回 JSON 解析失败",
			Err:        err,
		}
	}

	if hasWeiboChallengeField(top) {
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindRiskControl,
			HTTPStatus: in.StatusCode,
			Endpoint:   endpoint,
			Message:    "微博触发验证码或安全挑战，已立即停止请求",
		}
	}

	if okRaw, hasOK := top["ok"]; hasOK {
		okVal, validOK := parseStrictJSONInt(okRaw)
		if !validOK {
			return &site.Error{
				Site:       site.Weibo,
				Kind:       site.ErrKindSchemaChanged,
				HTTPStatus: in.StatusCode,
				Endpoint:   endpoint,
				Message:    "微博响应 ok 字段类型异常",
			}
		}
		if okVal == 1 {
			return nil
		}
		return classifyWeiboOKError(endpoint, in.StatusCode, okVal, top)
	}

	if codeRaw, hasCode := top["code"]; hasCode {
		codeStr := strings.TrimSpace(rawNumberOrString(codeRaw))
		if codeStr == "100000" {
			return nil
		}
		return classifyComponentCodeError(endpoint, in.StatusCode, codeStr, top)
	}

	return &site.Error{
		Site:       site.Weibo,
		Kind:       site.ErrKindSchemaChanged,
		HTTPStatus: in.StatusCode,
		Endpoint:   endpoint,
		Message:    "微博响应缺少预期信封字段",
	}
}

func classifyRedirectError(endpoint string, statusCode int, rawLocation string, hasCookie bool) *site.Error {
	loc := strings.ToLower(strings.TrimSpace(rawLocation))

	if isRiskRedirectLocation(loc) {
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindRiskControl,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "微博接口跳转至安全验证页，已立即停止请求",
		}
	}
	if isVisitorRedirectLocation(loc) {
		if hasCookie {
			// Authenticated request kicked back to Sina Visitor System -> risk_control (weibo.md §9.2).
			return &site.Error{
				Site:       site.Weibo,
				Kind:       site.ErrKindRiskControl,
				HTTPStatus: statusCode,
				Endpoint:   endpoint,
				Message:    "微博接口将当前会话重定向至访客校验页，已停止请求",
			}
		}
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindAuthRequired,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "微博接口要求登录（跳转至访客页）",
		}
	}
	if isLoginRedirectLocation(loc) {
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindAuthRequired,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "微博登录状态已失效（跳转至登录页），请重新粘贴 Cookie",
		}
	}
	return &site.Error{
		Site:       site.Weibo,
		Kind:       site.ErrKindRiskControl,
		HTTPStatus: statusCode,
		Endpoint:   endpoint,
		Message:    fmt.Sprintf("微博接口返回异常跳转（HTTP %d），已停止请求", statusCode),
	}
}

func classifyNonJSONFake200(endpoint string, statusCode int, body []byte, hasCookie bool) *site.Error {
	snippet := body
	if len(snippet) > 8192 {
		snippet = snippet[:8192]
	}
	lower := strings.ToLower(string(snippet))
	raw := string(snippet)

	if containsAny(lower, "geetest", "captcha", "slider", "security/verify", "verify_code", "waf") ||
		containsAny(raw, "滑块", "安全验证", "验证码", "极验", "行为验证") {
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindRiskControl,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "微博返回了验证码验证页面，已立即停止请求",
		}
	}
	if containsAny(lower, "sina visitor system", "genvisitor", "visitor.passport", "passport.weibo.com/visitor") {
		if !hasCookie {
			return &site.Error{
				Site:       site.Weibo,
				Kind:       site.ErrKindAuthRequired,
				HTTPStatus: statusCode,
				Endpoint:   endpoint,
				Message:    "未登录微博（返回 Sina Visitor System 访客页）",
			}
		}
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindRiskControl,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "微博返回 Sina Visitor System 访客验证页，已立即停止请求",
		}
	}
	if containsAny(lower, "weibo.com/newlogin", "login.sina.com.cn", "passport.weibo.com/sso", "newlogin") ||
		containsAny(raw, "微博登录", "请先登录", "登录微博") {
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindAuthRequired,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "微博登录状态已失效（返回登录页面），请重新粘贴 Cookie",
		}
	}
	return &site.Error{
		Site:       site.Weibo,
		Kind:       site.ErrKindRiskControl,
		HTTPStatus: statusCode,
		Endpoint:   endpoint,
		Message:    "微博返回了非 JSON 页面，疑似触发拦截或验证，已停止请求",
	}
}

func tryClassifyExplicitForbiddenJSON(endpoint string, statusCode int, body []byte) *site.Error {
	var top map[string]json.RawMessage
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&top); err != nil {
		return nil
	}
	if hasWeiboChallengeField(top) {
		return nil
	}
	msg := extractEnvelopeMessage(top)
	redirURL := strings.ToLower(extractEnvelopeURL(top))
	if isRiskRedirectLocation(redirURL) || isVisitorRedirectLocation(redirURL) || isRiskMessage(msg) {
		return nil
	}
	code := extractBizErrorCode(top)
	switch code {
	case 20003:
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindUnavailable,
			Code:       code,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "该微博用户不存在或已注销",
		}
	case 20101:
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindUnavailable,
			Code:       code,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "该微博不存在或已被作者删除",
		}
	case 20112:
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindForbidden,
			Code:       code,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "没有这条微博的查看权限",
		}
	}
	if isForbiddenMessage(msg) {
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindForbidden,
			Code:       code,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    msg,
		}
	}
	if isUnavailableMessage(msg) {
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindUnavailable,
			Code:       code,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    msg,
		}
	}
	return nil
}

func classifyWeiboOKError(endpoint string, statusCode int, okVal int, top map[string]json.RawMessage) *site.Error {
	msg := extractEnvelopeMessage(top)
	redirURL := strings.ToLower(extractEnvelopeURL(top))
	bizCode := extractBizErrorCode(top)

	if okVal == -100 {
		if isRiskRedirectLocation(redirURL) || isRiskMessage(msg) {
			return &site.Error{
				Site:       site.Weibo,
				Kind:       site.ErrKindRiskControl,
				Code:       okVal,
				HTTPStatus: statusCode,
				Endpoint:   endpoint,
				Message:    "微博触发安全验证，已立即停止请求",
			}
		}
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindAuthRequired,
			Code:       okVal,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "微博登录状态已失效，请重新粘贴 Cookie 登录",
		}
	}

	if isRiskRedirectLocation(redirURL) || isVisitorRedirectLocation(redirURL) || isRiskMessage(msg) {
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindRiskControl,
			Code:       firstNonZero(bizCode, okVal),
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "微博触发风控或验证拦截，已立即停止请求",
		}
	}

	if isRateLimitCode(bizCode) {
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindRateLimited,
			Code:       firstNonZero(bizCode, okVal),
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "微博请求过于频繁，已停止请求并进入退避",
		}
	}

	if isTransientServerBusyMessage(msg) {
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindNetwork,
			Code:       firstNonZero(bizCode, okVal),
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "微博服务端暂时繁忙，请稍后重试",
		}
	}

	if isRateLimitMessage(msg) {
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindRateLimited,
			Code:       firstNonZero(bizCode, okVal),
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "微博请求过于频繁，已停止请求并进入退避",
		}
	}

	if isLoginRedirectLocation(redirURL) || isLoginMessage(msg) {
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindAuthRequired,
			Code:       firstNonZero(bizCode, okVal),
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "微博登录状态已失效，请重新粘贴 Cookie 登录",
		}
	}

	switch bizCode {
	case 20003:
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindUnavailable,
			Code:       bizCode,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "该微博用户不存在或已注销",
		}
	case 20101:
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindUnavailable,
			Code:       bizCode,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "该微博不存在或已被作者删除",
		}
	case 20112:
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindForbidden,
			Code:       bizCode,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "没有该微博的查看权限",
		}
	}

	if isForbiddenMessage(msg) {
		outMsg := msg
		if outMsg == "" {
			outMsg = "该微博内容受权限限制，暂无法查看"
		}
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindForbidden,
			Code:       firstNonZero(bizCode, okVal),
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    outMsg,
		}
	}

	if isUnavailableMessage(msg) {
		outMsg := msg
		if outMsg == "" {
			outMsg = "目标微博或用户不存在或已删除"
		}
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindUnavailable,
			Code:       firstNonZero(bizCode, okVal),
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    outMsg,
		}
	}

	outMsg := msg
	if outMsg == "" {
		outMsg = fmt.Sprintf("微博接口返回业务失败（ok=%d）", okVal)
	}
	return &site.Error{
		Site:       site.Weibo,
		Kind:       site.ErrKindUnavailable,
		Code:       firstNonZero(bizCode, okVal),
		HTTPStatus: statusCode,
		Endpoint:   endpoint,
		Message:    outMsg,
	}
}

func classifyComponentCodeError(endpoint string, statusCode int, codeStr string, top map[string]json.RawMessage) *site.Error {
	msg := extractEnvelopeMessage(top)
	redirURL := strings.ToLower(extractEnvelopeURL(top))
	numCode, _ := strconv.Atoi(codeStr)

	if isRiskRedirectLocation(redirURL) || isVisitorRedirectLocation(redirURL) || isRiskMessage(msg) {
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindRiskControl,
			Code:       numCode,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "微博视频组件触发风控或验证拦截，已立即停止请求",
		}
	}
	if isTransientServerBusyMessage(msg) {
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindNetwork,
			Code:       numCode,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "微博视频组件服务端暂时繁忙，请稍后重试",
		}
	}
	if isRateLimitMessage(msg) {
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindRateLimited,
			Code:       numCode,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "微博视频组件请求过于频繁，已停止请求",
		}
	}
	if codeStr == "100006" || isLoginRedirectLocation(redirURL) || isLoginMessage(msg) {
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindAuthRequired,
			Code:       numCode,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    "微博登录状态已失效，请重新粘贴 Cookie 登录",
		}
	}
	if isForbiddenMessage(msg) {
		return &site.Error{
			Site:       site.Weibo,
			Kind:       site.ErrKindForbidden,
			Code:       numCode,
			HTTPStatus: statusCode,
			Endpoint:   endpoint,
			Message:    msg,
		}
	}
	outMsg := msg
	if outMsg == "" {
		outMsg = fmt.Sprintf("微博视频组件返回失败（code=%s）", codeStr)
	}
	return &site.Error{
		Site:       site.Weibo,
		Kind:       site.ErrKindUnavailable,
		Code:       numCode,
		HTTPStatus: statusCode,
		Endpoint:   endpoint,
		Message:    outMsg,
	}
}

func hasWeiboChallengeField(top map[string]json.RawMessage) bool {
	for _, key := range []string{"geetest", "captcha", "verify_url"} {
		raw, ok := top[key]
		if !ok {
			continue
		}
		t := bytes.TrimSpace(raw)
		if len(t) == 0 || bytes.Equal(t, []byte("null")) || bytes.Equal(t, []byte(`""`)) || bytes.Equal(t, []byte("{}")) || bytes.Equal(t, []byte("[]")) || bytes.Equal(t, []byte("false")) {
			continue
		}
		return true
	}
	if u := strings.ToLower(extractEnvelopeURL(top)); isRiskRedirectLocation(u) {
		return true
	}
	return false
}

func isVisitorRedirectLocation(loc string) bool {
	return containsAny(loc,
		"visitor.passport.weibo.cn",
		"passport.weibo.com/visitor",
		"/visitor/visitor",
		"/visitor/genvisitor",
	)
}

func isLoginRedirectLocation(loc string) bool {
	if isVisitorRedirectLocation(loc) {
		return false
	}
	return containsAny(loc,
		"weibo.com/newlogin",
		"login.sina.com.cn",
		"passport.weibo.com",
		"passport.weibo.cn",
		"/newlogin",
		"/signin/login",
		"/login.php",
	)
}

func isRiskRedirectLocation(loc string) bool {
	return containsAny(loc,
		"geetest",
		"captcha",
		"slider",
		"security/verify",
		"verify.weibo",
		"captcha.weibo",
		"security/validate",
		"captcha/validate",
	)
}

func isRiskMessage(msg string) bool {
	lower := strings.ToLower(msg)
	return containsAny(lower, "geetest", "captcha", "slider") ||
		containsAny(msg, "验证码", "安全验证", "滑块", "异常访问", "账号异常", "行为验证", "极验")
}

func isRateLimitCode(code int) bool {
	switch code {
	case 10022, 10023, 10024:
		return true
	default:
		return false
	}
}

func isRateLimitMessage(msg string) bool {
	lower := strings.ToLower(msg)
	return containsAny(lower, "too many", "rate limit", "frequently") ||
		containsAny(msg, "频繁", "稍后再试", "请求过多", "访问太快", "操作过快", "次数超限")
}

func isTransientServerBusyMessage(msg string) bool {
	lower := strings.ToLower(msg)
	return containsAny(lower, "server busy", "system busy") ||
		containsAny(msg, "系统繁忙", "服务繁忙")
}

func isLoginMessage(msg string) bool {
	lower := strings.ToLower(msg)
	return containsAny(lower, "not login", "login required", "please login") ||
		containsAny(msg, "未登录", "请先登录", "请登录", "登录失效", "重新登录", "登录状态已失效")
}

func isUnavailableMessage(msg string) bool {
	return containsAny(msg,
		"已被作者删除",
		"微博不存在",
		"用户不存在",
		"不存在",
		"已删除",
		"已注销",
		"暂不可查看",
		"内容已删除",
		"已经被删除",
		"该微博已删除",
		"账号已注销",
		"已失效",
	)
}

func isForbiddenMessage(msg string) bool {
	return containsAny(msg,
		"仅粉丝可见",
		"好友圈",
		"仅博主自己可见",
		"仅自己可见",
		"无权查看",
		"关注后可见",
		"没有查看权限",
		"查看权限",
		"由于作者设置",
		"仅互粉可见",
		"隐私设置",
		"黑名单",
		"拉黑",
		"暂没有权限",
	)
}

func extractEnvelopeMessage(top map[string]json.RawMessage) string {
	for _, key := range []string{"msg", "message", "error", "error_msg"} {
		if s := parseJSONString(top[key]); s != "" {
			return s
		}
	}
	if dataRaw, ok := top["data"]; ok {
		var dataMap map[string]json.RawMessage
		if json.Unmarshal(dataRaw, &dataMap) == nil {
			for _, key := range []string{"msg", "message", "error"} {
				if s := parseJSONString(dataMap[key]); s != "" {
					return s
				}
			}
		}
	}
	return ""
}

func extractEnvelopeURL(top map[string]json.RawMessage) string {
	for _, key := range []string{"url", "redirect_url", "verify_url"} {
		if s := parseJSONString(top[key]); s != "" {
			return s
		}
	}
	if dataRaw, ok := top["data"]; ok {
		var dataMap map[string]json.RawMessage
		if json.Unmarshal(dataRaw, &dataMap) == nil {
			for _, key := range []string{"url", "redirect_url", "verify_url"} {
				if s := parseJSONString(dataMap[key]); s != "" {
					return s
				}
			}
		}
	}
	return ""
}

func extractBizErrorCode(top map[string]json.RawMessage) int {
	for _, key := range []string{"error_code", "errno"} {
		if raw, ok := top[key]; ok {
			if v, valid := parseStrictJSONInt(raw); valid && v != 0 {
				return v
			}
		}
	}
	if dataRaw, ok := top["data"]; ok {
		var dataMap map[string]json.RawMessage
		if json.Unmarshal(dataRaw, &dataMap) == nil {
			for _, key := range []string{"error_code", "errno"} {
				if raw, ok := dataMap[key]; ok {
					if v, valid := parseStrictJSONInt(raw); valid && v != 0 {
						return v
					}
				}
			}
		}
	}
	return 0
}

func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if sub != "" && strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func firstNonZero(vals ...int) int {
	for _, v := range vals {
		if v != 0 {
			return v
		}
	}
	return 0
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
	if f, err := strconv.ParseFloat(s, 64); err == nil {
		return int64(f)
	}
	return 0
}

// parseJSONInt is parseJSONInt64 bounded to the 32-bit range, so the result
// fits int on every platform. Counts, sizes and type codes never come close.
func parseJSONInt(raw json.RawMessage) int {
	v := parseJSONInt64(raw)
	if v > math.MaxInt32 {
		return math.MaxInt32
	}
	if v < math.MinInt32 {
		return math.MinInt32
	}
	return int(v)
}

func parseJSONFloat64(raw json.RawMessage) float64 {
	s := rawNumberOrString(raw)
	if s == "" {
		return 0
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

func parseJSONBoolOrInt(raw json.RawMessage) bool {
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
	var s string
	if json.Unmarshal(t, &s) == nil {
		s = strings.ToLower(strings.TrimSpace(s))
		return s == "true" || s == "1"
	}
	return false
}

func isJSONNull(raw json.RawMessage) bool {
	t := bytes.TrimSpace(raw)
	return len(t) == 0 || bytes.Equal(t, []byte("null"))
}
