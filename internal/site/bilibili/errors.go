package bilibili

import (
	"fmt"

	"github.com/chialecode/trace/internal/site"
)

// Unified error classifications returned by the Bilibili site adapter.
const (
	ErrKindAuthRequired  = site.ErrKindAuthRequired
	ErrKindRiskControl   = site.ErrKindRiskControl
	ErrKindRateLimited   = site.ErrKindRateLimited
	ErrKindBlocked       = site.ErrKindBlocked
	ErrKindUnavailable   = site.ErrKindUnavailable
	ErrKindForbidden     = site.ErrKindForbidden
	ErrKindBadRequest    = site.ErrKindBadRequest
	ErrKindSchemaChanged = site.ErrKindSchemaChanged
	ErrKindNetwork       = site.ErrKindNetwork
	ErrKindQuota         = site.ErrKindQuota
	ErrKindBackoff       = site.ErrKindBackoff
)

// SiteError aliases site.Error so cross-site managers and Bilibili callers share one error type.
type SiteError = site.Error

// KindOf extracts the ErrorKind from err, defaulting to ErrKindNetwork.
func KindOf(err error) string {
	return site.KindOf(err)
}

// IsKind reports whether err is a SiteError of the specified kind.
func IsKind(err error, kind string) bool {
	return site.IsKind(err, kind)
}

func classifyBiliCode(code int, message string, hasVoucher bool, endpoint string) *SiteError {
	if hasVoucher {
		return &SiteError{
			Kind:     ErrKindRiskControl,
			Code:     code,
			Endpoint: endpoint,
			Message:  "触发站点风控验证 (v_voucher)，已停止自动请求",
		}
	}
	switch code {
	case -101:
		return &SiteError{
			Kind:     ErrKindAuthRequired,
			Code:     code,
			Endpoint: endpoint,
			Message:  "账号未登录或登录态已失效，请重新登录",
		}
	case -352, -412, -509:
		return &SiteError{
			Kind:     ErrKindRiskControl,
			Code:     code,
			Endpoint: endpoint,
			Message:  "请求触发风控拦截，已停止自动请求",
		}
	case -799:
		return &SiteError{
			Kind:     ErrKindRateLimited,
			Code:     code,
			Endpoint: endpoint,
			Message:  "请求过于频繁，已暂停后续请求",
		}
	case 203:
		return &SiteError{
			Kind:     ErrKindBlocked,
			Code:     code,
			Endpoint: endpoint,
			Message:  "该内容已加密或受限，不支持打开或缓存",
		}
	case -403:
		return &SiteError{
			Kind:     ErrKindForbidden,
			Code:     code,
			Endpoint: endpoint,
			Message:  "当前账号无访问权限",
		}
	case -404, 62002, 62004, 62012:
		return &SiteError{
			Kind:     ErrKindUnavailable,
			Code:     code,
			Endpoint: endpoint,
			Message:  "目标内容不存在或已下架",
		}
	case -400:
		return &SiteError{
			Kind:     ErrKindBadRequest,
			Code:     code,
			Endpoint: endpoint,
			Message:  "请求参数无效",
		}
	default:
		return &SiteError{
			Kind:     ErrKindUnavailable,
			Code:     code,
			Endpoint: endpoint,
			Message:  "站点返回了未识别的错误代码",
		}
	}
}

func classifyHTTPStatus(status int, endpoint string) *SiteError {
	switch status {
	case 401:
		return &SiteError{
			Kind:       ErrKindAuthRequired,
			HTTPStatus: status,
			Endpoint:   endpoint,
			Message:    "需要登录凭证 (HTTP 401)",
		}
	case 403:
		return &SiteError{
			Kind:       ErrKindForbidden,
			HTTPStatus: status,
			Endpoint:   endpoint,
			Message:    "站点拒绝访问 (HTTP 403)",
		}
	case 404:
		return &SiteError{
			Kind:       ErrKindUnavailable,
			HTTPStatus: status,
			Endpoint:   endpoint,
			Message:    "资源不存在 (HTTP 404)",
		}
	case 412:
		return &SiteError{
			Kind:       ErrKindRiskControl,
			HTTPStatus: status,
			Endpoint:   endpoint,
			Message:    "触发站点风控拦截 (HTTP 412)，已停止请求",
		}
	case 429:
		return &SiteError{
			Kind:       ErrKindRateLimited,
			HTTPStatus: status,
			Endpoint:   endpoint,
			Message:    "触发频率限制 (HTTP 429)，已停止请求",
		}
	default:
		return &SiteError{
			Kind:       ErrKindNetwork,
			HTTPStatus: status,
			Endpoint:   endpoint,
			Message:    fmt.Sprintf("HTTP 状态异常 (%d)", status),
		}
	}
}
