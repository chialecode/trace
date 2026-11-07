package bilibili

import (
	"errors"
	"fmt"
)

// Unified error classifications returned by the Bilibili site adapter.
const (
	ErrKindAuthRequired  = "auth_required"
	ErrKindRiskControl   = "risk_control"
	ErrKindRateLimited   = "rate_limited"
	ErrKindBlocked       = "blocked"
	ErrKindUnavailable   = "unavailable"
	ErrKindForbidden     = "forbidden"
	ErrKindBadRequest    = "bad_request"
	ErrKindSchemaChanged = "schema_changed"
	ErrKindNetwork       = "network"
	ErrKindQuota         = "quota"
	ErrKindBackoff       = "backoff"
)

// SiteError is a structured error returned by Bilibili adapter methods.
type SiteError struct {
	Kind       string `json:"kind"`
	Code       int    `json:"code,omitempty"`
	HTTPStatus int    `json:"httpStatus,omitempty"`
	Endpoint   string `json:"endpoint,omitempty"`
	Message    string `json:"message"`
	Err        error  `json:"-"`
}

func (e *SiteError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code != 0 {
		return fmt.Sprintf("bilibili [%s code=%d]: %s", e.Kind, e.Code, e.Message)
	}
	if e.HTTPStatus != 0 {
		return fmt.Sprintf("bilibili [%s http=%d]: %s", e.Kind, e.HTTPStatus, e.Message)
	}
	return fmt.Sprintf("bilibili [%s]: %s", e.Kind, e.Message)
}

func (e *SiteError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// KindOf extracts the ErrorKind from err, defaulting to ErrKindNetwork.
func KindOf(err error) string {
	if err == nil {
		return ""
	}
	var se *SiteError
	if errors.As(err, &se) && se.Kind != "" {
		return se.Kind
	}
	return ErrKindNetwork
}

// IsKind reports whether err is a SiteError of the specified kind.
func IsKind(err error, kind string) bool {
	var se *SiteError
	return errors.As(err, &se) && se.Kind == kind
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
