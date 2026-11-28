package companionclient

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/chialecode/trace/internal/companionapi"
)

func classifyHTTPStatus(status int, hdr http.Header, raw []byte, clientVersion string) *Error {
	var env companionapi.ErrorBody
	_ = json.Unmarshal(raw, &env)

	code := strings.ToLower(strings.TrimSpace(env.Error.Code))
	msg := strings.TrimSpace(env.Error.Message)
	sv := firstNonEmpty(env.Error.ServerVersion, hdr.Get(companionapi.HeaderVersion))
	sp := firstNonEmpty(env.Error.ServerProtocol, hdr.Get(companionapi.HeaderProtocol))

	e := &Error{
		Code:           code,
		Site:           strings.TrimSpace(env.Error.Site),
		Message:        msg,
		ServerVersion:  sv,
		ServerProtocol: sp,
		ClientVersion:  clientVersion,
		ClientProtocol: companionapi.ProtocolVersion,
		Status:         status,
	}

	switch status {
	case http.StatusUnauthorized:
		if code == companionapi.CodeInvalidCode {
			e.Kind = KindBadPairingCode
		} else {
			e.Kind = KindUnauthorized
		}
	case http.StatusForbidden:
		switch code {
		case companionapi.CodeInvalidCode:
			e.Kind = KindBadPairingCode
		case "", companionapi.CodeForbiddenSource:
			e.Kind = KindForbiddenSource
		default:
			e.Kind = KindServerError
		}
	case http.StatusTooManyRequests:
		e.Kind = KindRateLimited
	case http.StatusConflict:
		switch code {
		case companionapi.CodeProtocolMismatch:
			e.Kind = KindProtocolMismatch
			e.UpdateSide = inferUpdateSide(sp, companionapi.ProtocolVersion)
		case companionapi.CodeChannelMismatch:
			e.Kind = KindChannelMismatch
		case companionapi.CodeDeviceLimit:
			e.Kind = KindDeviceLimit
		default:
			e.Kind = KindServerError
		}
	case http.StatusBadRequest, http.StatusGone:
		if code == companionapi.CodeInvalidCode || code == companionapi.CodePairExpired || strings.Contains(code, "code") {
			e.Kind = KindBadPairingCode
		} else {
			e.Kind = KindServerError
		}
	default:
		e.Kind = KindServerError
	}
	return e
}

func inferUpdateSide(serverProto, clientProto string) string {
	if serverProto == "" || clientProto == "" || serverProto == clientProto {
		return "mobile"
	}
	if len(serverProto) > len(clientProto) || (len(serverProto) == len(clientProto) && serverProto > clientProto) {
		return "mobile"
	}
	return "desktop"
}
