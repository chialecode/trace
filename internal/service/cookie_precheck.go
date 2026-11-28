package service

import (
	"errors"
	"strings"

	"github.com/chialecode/trace/internal/credential"
	"github.com/chialecode/trace/internal/site"
)

// CookiePrecheckSeverity classifies the local pre-check status of a pasted Cookie string.
// Pre-check feedback is strictly informational / non-blocking and never echoes raw values.
type CookiePrecheckSeverity string

const (
	CookiePrecheckEmpty   CookiePrecheckSeverity = "empty"
	CookiePrecheckOK      CookiePrecheckSeverity = "ok"
	CookiePrecheckWarning CookiePrecheckSeverity = "warning"
	CookiePrecheckError   CookiePrecheckSeverity = "error"
)

// CookiePrecheckField describes one required or key auxiliary Cookie key without any value.
type CookiePrecheckField struct {
	Key    string
	Reason string
}

// CookiePrecheckResult summarizes local pre-check findings for a pasted Cookie string.
// It contains only canonical key names and static guidance text — never raw cookie values.
type CookiePrecheckResult struct {
	Site            string
	Severity        CookiePrecheckSeverity
	Summary         string
	MissingRequired []CookiePrecheckField
	MissingOptional []CookiePrecheckField
	Notices         []string
	Guidance        string
}

var douyinUIFIDKeyVariants = [...]string{
	"uifid",
	"uifid_temp",
	"uifidtemp",
	"UIFID",
	"UIFID_TEMP",
	"UIFIDTEMP",
}

// PrecheckCookieInput performs a pure local, zero-network, zero-logging pre-check
// of a user-pasted Cookie string for siteID ("bilibili", "douyin", "weibo", or "x").
// It checks required fields, input bounds, control-character/format rules, and known
// site-specific key auxiliary fields, never echoing any pasted secret values.
func PrecheckCookieInput(siteID, rawCookie string) CookiePrecheckResult {
	id, err := normalizeSiteID(siteID)
	if err != nil {
		return CookiePrecheckResult{
			Site:     strings.TrimSpace(siteID),
			Severity: CookiePrecheckError,
			Summary:  "不支持的站点标识",
		}
	}
	res := CookiePrecheckResult{
		Site:     string(id),
		Severity: CookiePrecheckEmpty,
	}
	if strings.TrimSpace(rawCookie) == "" {
		return res
	}
	if len(rawCookie) > credential.MaxCookieInputBytes {
		res.Severity = CookiePrecheckError
		res.Summary = "粘贴内容超过 64 KiB 上限，请仅复制请求头中的 Cookie 字段值"
		res.Guidance = precheckCopyGuidance(id)
		return res
	}

	switch id {
	case site.Douyin:
		return precheckDouyinCookie(rawCookie)
	case site.Weibo:
		return precheckWeiboCookie(rawCookie)
	case site.X:
		return precheckXCookie(rawCookie)
	default:
		return precheckBilibiliCookie(rawCookie)
	}
}

func precheckDouyinCookie(rawCookie string) CookiePrecheckResult {
	res := CookiePrecheckResult{Site: string(site.Douyin)}
	cleaned, err := credential.ParseDouyinCookieInput(rawCookie)
	if err != nil {
		res.Severity = CookiePrecheckError
		res.Guidance = precheckCopyGuidance(site.Douyin)
		switch {
		case errors.Is(err, credential.ErrInputTooLarge):
			res.Summary = "粘贴内容超过 64 KiB 上限，请仅复制请求头中的 Cookie 字段值"
		case errors.Is(err, credential.ErrCleanedCookieTooLarge):
			res.Summary = "清洗后的 Cookie 超过 32 KiB 上限，请仅复制当前抖音标签页请求头中的 Cookie"
		default:
			_, lowerKeys, pairCount := inspectRawCookieKeys(rawCookie)
			res.MissingRequired = []CookiePrecheckField{
				{Key: "sessionid / sessionid_ss", Reason: "核心登录态字段，缺少时无法验证抖音账号"},
			}
			res.Summary = "缺少必填字段 sessionid（或 sessionid_ss），验证会失败"
			if pairCount == 0 {
				res.Notices = append(res.Notices, "未识别到 key=value 形式的 Cookie 字段，请确认复制的是请求头中的 Cookie 值")
			} else if lowerKeys["ttwid"] || lowerKeys["mstoken"] {
				res.Notices = append(res.Notices, "当前仅检测到访客态字段（如 ttwid / msToken），请先在浏览器登录抖音账号后再重新复制")
			}
		}
		return res
	}

	if cleaned["s_v_web_id"] == "" {
		res.MissingOptional = append(res.MissingOptional, CookiePrecheckField{
			Key:    "s_v_web_id",
			Reason: "缺少时「我的 › 抖音 › 喜欢」等风控较严的接口容易返回空数据",
		})
	}
	if cleaned["ttwid"] == "" {
		res.MissingOptional = append(res.MissingOptional, CookiePrecheckField{
			Key:    "ttwid",
			Reason: "缺少时抖音直播间状态检查与录制可能失败或触发风控",
		})
	}
	if !hasAnyExactCookieKey(cleaned, douyinUIFIDKeyVariants[:]) {
		res.MissingOptional = append(res.MissingOptional, CookiePrecheckField{
			Key:    "UIFID",
			Reason: "缺少时作品详情、创作者主页等受保护接口的签名更容易被拒",
		})
	}

	if len(res.MissingOptional) > 0 {
		res.Severity = CookiePrecheckWarning
		res.Summary = "已检测到登录态，但缺少关键辅助字段（" + joinPrecheckKeys(res.MissingOptional) + "），部分功能可能异常（仍可直接提交）"
		res.Guidance = precheckCopyGuidance(site.Douyin)
		return res
	}

	res.Severity = CookiePrecheckOK
	res.Summary = "预检通过：已检测到 sessionid 及关键辅助字段（s_v_web_id、ttwid、UIFID）"
	return res
}

func precheckWeiboCookie(rawCookie string) CookiePrecheckResult {
	res := CookiePrecheckResult{Site: string(site.Weibo)}
	cleaned, err := credential.ParseWeiboCookieInput(rawCookie)
	if err != nil {
		res.Severity = CookiePrecheckError
		res.Guidance = precheckCopyGuidance(site.Weibo)
		switch {
		case errors.Is(err, credential.ErrInputTooLarge):
			res.Summary = "粘贴内容超过 64 KiB 上限，请仅复制请求头中的 Cookie 字段值"
		case errors.Is(err, credential.ErrCleanedCookieTooLarge):
			res.Summary = "清洗后的 Cookie 超过 32 KiB 上限，请仅复制单条请求头中的 Cookie"
		case errors.Is(err, credential.ErrInvalidCookieControlChar):
			res.Summary = "粘贴内容包含非法控制字符或错误换行格式，请只复制请求头中的 Cookie 值"
		default:
			exactKeys, lowerKeys, pairCount := inspectRawCookieKeys(rawCookie)
			res.MissingRequired = []CookiePrecheckField{
				{Key: "SUB", Reason: "微博核心登录态字段（区分大小写），缺少时无法验证账号"},
			}
			res.Summary = "缺少必填字段 SUB（区分大小写），验证会失败"
			if pairCount == 0 {
				res.Notices = append(res.Notices, "未识别到 key=value 形式的 Cookie 字段，请确认复制的是请求头中的 Cookie 值")
			} else if !exactKeys["SUB"] && lowerKeys["sub"] {
				res.Notices = append(res.Notices, "检测到非大写的 sub 字段；微博 Cookie 区分大小写，请保留原始大写 SUB")
			}
		}
		return res
	}

	exactKeys, lowerKeys, _ := inspectRawCookieKeys(rawCookie)
	if cleaned["SUBP"] == "" {
		res.MissingOptional = append(res.MissingOptional, CookiePrecheckField{
			Key:    "SUBP",
			Reason: "伴随登录态字段，缺少时部分页面接口兼容性可能下降",
		})
	}
	if cleaned["XSRF-TOKEN"] == "" {
		res.MissingOptional = append(res.MissingOptional, CookiePrecheckField{
			Key:    "XSRF-TOKEN",
			Reason: "PC 端 Ajax 接口校验所需字段，缺少时部分列表接口可能受限",
		})
		if !exactKeys["XSRF-TOKEN"] && lowerKeys["xsrf-token"] {
			res.Notices = append(res.Notices, "检测到非大写的 xsrf-token 字段；请保留原始大写 XSRF-TOKEN")
		}
	}
	if cleaned["WBPSESS"] == "" {
		res.MissingOptional = append(res.MissingOptional, CookiePrecheckField{
			Key:    "WBPSESS",
			Reason: "PC 版 weibo.com 会话字段，从手机版 m.weibo.cn 复制时常会缺失",
		})
	}

	if len(res.MissingOptional) > 0 {
		res.Severity = CookiePrecheckWarning
		res.Summary = "已检测到 SUB，但缺少关键辅助字段（" + joinPrecheckKeys(res.MissingOptional) + "），部分接口可能受限（仍可直接提交）"
		res.Guidance = precheckCopyGuidance(site.Weibo)
		return res
	}

	res.Severity = CookiePrecheckOK
	res.Summary = "预检通过：已检测到 SUB 及关键辅助字段（SUBP、XSRF-TOKEN、WBPSESS）"
	return res
}

func precheckXCookie(rawCookie string) CookiePrecheckResult {
	res := CookiePrecheckResult{Site: string(site.X)}
	cleaned, err := credential.ParseXCookieInput(rawCookie)
	if err != nil {
		res.Severity = CookiePrecheckError
		res.Guidance = precheckCopyGuidance(site.X)
		switch {
		case errors.Is(err, credential.ErrInputTooLarge):
			res.Summary = "粘贴内容超过 64 KiB 上限，请仅复制请求头中的 Cookie 字段值"
		case errors.Is(err, credential.ErrCleanedCookieTooLarge):
			res.Summary = "清洗后的 Cookie 超过 32 KiB 上限，请仅复制 x.com 请求头中的 Cookie"
		case errors.Is(err, credential.ErrInvalidCookieControlChar):
			res.Summary = "粘贴内容包含非法控制字符或错误换行格式，请只复制请求头中的 Cookie 值"
		case errors.Is(err, credential.ErrInvalidXCT0):
			res.Summary = "ct0 格式无效，验证会失败"
			res.Notices = append(res.Notices, "ct0 仅允许英文字母与数字，请确认复制了完整的原始 ct0 字段")
		default:
			exactKeys, lowerKeys, pairCount := inspectRawCookieKeys(rawCookie)
			if !exactKeys["auth_token"] {
				res.MissingRequired = append(res.MissingRequired, CookiePrecheckField{
					Key:    "auth_token",
					Reason: "X 核心登录态字段（区分大小写），缺少时无法验证账号",
				})
				if lowerKeys["auth_token"] {
					res.Notices = append(res.Notices, "检测到非小写的 auth_token 字段；X Cookie 区分大小写，请保留原始小写 auth_token")
				}
			}
			if !exactKeys["ct0"] {
				res.MissingRequired = append(res.MissingRequired, CookiePrecheckField{
					Key:    "ct0",
					Reason: "X CSRF 校验字段（需与 auth_token 同时提供）",
				})
				if lowerKeys["ct0"] {
					res.Notices = append(res.Notices, "检测到非小写的 ct0 字段；X Cookie 区分大小写，请保留原始小写 ct0")
				}
			}
			if len(res.MissingRequired) > 0 {
				res.Summary = "缺少必填字段 " + joinPrecheckKeys(res.MissingRequired) + "，验证会失败"
			} else {
				res.Summary = "缺少必填字段 auth_token 或 ct0，验证会失败"
			}
			if pairCount == 0 {
				res.Notices = append(res.Notices, "未识别到 key=value 形式的 Cookie 字段，请确认复制的是请求头中的 Cookie 值")
			}
		}
		return res
	}

	if rawTWID := strings.TrimSpace(cleaned["twid"]); rawTWID != "" {
		if _, twErr := credential.ParseXTWID(rawTWID); twErr != nil {
			res.Severity = CookiePrecheckError
			res.Summary = "twid 格式无效，验证会失败"
			res.Notices = append(res.Notices, "twid 应为 u=<数字ID>（或 URL 编码的 u%3D<数字ID>），请重新从请求头复制完整 Cookie")
			res.Guidance = precheckCopyGuidance(site.X)
			return res
		}
	} else {
		res.MissingOptional = append(res.MissingOptional, CookiePrecheckField{
			Key:    "twid",
			Reason: "用于核对当前登录账号身份，缺少时仅靠接口返回确认",
		})
	}
	if cleaned["guest_id"] == "" {
		res.MissingOptional = append(res.MissingOptional, CookiePrecheckField{
			Key:    "guest_id",
			Reason: "建议随完整请求头一同保留，缺少时部分接口更容易受限",
		})
	}

	if len(res.MissingOptional) > 0 {
		res.Severity = CookiePrecheckWarning
		res.Summary = "已检测到 auth_token 与 ct0，但缺少关键辅助字段（" + joinPrecheckKeys(res.MissingOptional) + "），建议复制完整 Cookie（仍可直接提交）"
		res.Guidance = precheckCopyGuidance(site.X)
		return res
	}

	res.Severity = CookiePrecheckOK
	res.Summary = "预检通过：已检测到 auth_token、ct0 及关键辅助字段（twid、guest_id）"
	return res
}

func precheckBilibiliCookie(rawCookie string) CookiePrecheckResult {
	res := CookiePrecheckResult{Site: string(site.Bilibili)}
	cleaned, _, err := credential.ParseManualCookieInput(rawCookie, "")
	if err != nil {
		res.Severity = CookiePrecheckError
		res.Guidance = precheckCopyGuidance(site.Bilibili)
		switch {
		case errors.Is(err, credential.ErrInputTooLarge):
			res.Summary = "粘贴内容超过 64 KiB 上限，请仅复制请求头中的 Cookie 字段值"
		case errors.Is(err, credential.ErrCleanedCookieTooLarge):
			res.Summary = "清洗后的 Cookie 超过 32 KiB 上限，请仅复制 bilibili.com 请求头中的 Cookie"
		default:
			_, _, pairCount := inspectRawCookieKeys(rawCookie)
			res.MissingRequired = []CookiePrecheckField{
				{Key: "SESSDATA", Reason: "哔哩哔哩核心登录态字段，缺少时无法验证账号"},
			}
			res.Summary = "缺少必填字段 SESSDATA，验证会失败"
			if pairCount == 0 {
				res.Notices = append(res.Notices, "未识别到 key=value 形式的 Cookie 字段，请确认复制的是请求头中的 Cookie 值")
			}
		}
		return res
	}

	if cleaned["bili_jct"] == "" {
		res.MissingOptional = append(res.MissingOptional, CookiePrecheckField{
			Key:    "bili_jct",
			Reason: "常用伴随校验字段，建议随完整请求头一同复制",
		})
	}
	if cleaned["DedeUserID"] == "" {
		res.MissingOptional = append(res.MissingOptional, CookiePrecheckField{
			Key:    "DedeUserID",
			Reason: "用于辅助核对账号 UID 一致性，建议随完整请求头一同复制",
		})
	}

	if len(res.MissingOptional) > 0 {
		res.Severity = CookiePrecheckWarning
		res.Summary = "已检测到 SESSDATA，但缺少关键辅助字段（" + joinPrecheckKeys(res.MissingOptional) + "），建议复制完整 Cookie（仍可直接提交）"
		res.Guidance = precheckCopyGuidance(site.Bilibili)
		return res
	}

	res.Severity = CookiePrecheckOK
	res.Summary = "预检通过：已检测到 SESSDATA 及关键辅助字段（bili_jct、DedeUserID）"
	return res
}

func precheckCopyGuidance(siteID site.ID) string {
	switch siteID {
	case site.Douyin:
		return "在桌面浏览器登录 douyin.com 并打开「我的 → 喜欢」（如有滑块先完成验证），按 F12 → 网络（Network）→ Fetch/XHR，刷新后从 www.douyin.com 请求头复制完整 Cookie。"
	case site.Weibo:
		return "在桌面浏览器打开 PC 版 weibo.com（勿从 m.weibo.cn 复制）并登录，按 F12 → 网络（Network）→ Fetch/XHR，刷新后从 weibo.com/ajax/ 请求头复制完整 Cookie。"
	case site.X:
		return "在桌面浏览器打开已登录的 x.com/home，按 F12 → 网络（Network）→ Fetch/XHR，刷新后从 x.com 请求头复制完整 Cookie（勿从 twimg.com 图片请求复制）。"
	default:
		return "日常推荐扫码登录（支持自动续期）；如需手动粘贴，请在已登录 bilibili.com 页面按 F12 → 网络（Network）→ Fetch/XHR，刷新后从 api.bilibili.com 请求头复制完整 Cookie。"
	}
}

// inspectRawCookieKeys scans only key names and non-empty presence from rawCookie.
// Values are never retained or returned.
func inspectRawCookieKeys(rawCookie string) (exact map[string]bool, lower map[string]bool, pairCount int) {
	exact = make(map[string]bool)
	lower = make(map[string]bool)
	cleaned := strings.TrimSpace(rawCookie)
	if strings.HasPrefix(strings.ToLower(cleaned), "cookie:") {
		cleaned = strings.TrimSpace(cleaned[len("cookie:"):])
	}
	cleaned = strings.ReplaceAll(cleaned, "\r", ";")
	cleaned = strings.ReplaceAll(cleaned, "\n", ";")
	for _, part := range strings.Split(cleaned, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		eq := strings.IndexByte(part, '=')
		if eq <= 0 {
			continue
		}
		k := strings.TrimSpace(part[:eq])
		v := strings.Trim(strings.TrimSpace(part[eq+1:]), `"'`)
		if k == "" || v == "" {
			continue
		}
		pairCount++
		exact[k] = true
		lower[strings.ToLower(k)] = true
	}
	return exact, lower, pairCount
}

func hasAnyExactCookieKey(cookies map[string]string, keys []string) bool {
	for _, k := range keys {
		if cookies[k] != "" {
			return true
		}
	}
	return false
}

func joinPrecheckKeys(fields []CookiePrecheckField) string {
	keys := make([]string, 0, len(fields))
	for _, f := range fields {
		if f.Key != "" {
			keys = append(keys, f.Key)
		}
	}
	return strings.Join(keys, "、")
}
