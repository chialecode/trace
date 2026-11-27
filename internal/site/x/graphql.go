package x

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/chialecode/trace/internal/site"
)

var allowedGraphQLOperations = map[string]bool{
	OpUserByScreenName:         true,
	OpUserByRestId:             true,
	OpUserTweets:               true,
	OpUserTweetsAndReplies:     true,
	OpUserMedia:                true,
	OpUserHighlightsTweets:     true,
	OpUserArticlesTweets:       true,
	OpTweetDetail:              true,
	OpFollowing:                true,
	OpBookmarks:                true,
	OpLikes:                    true,
	OpCombinedLists:            true,
	OpListOwnerships:           true,
	OpListByRestId:             true,
	OpListLatestTweetsTimeline: true,
	OpListMembers:              true,
}

// IsAllowedOperation reports whether opName is one of the read-only GraphQL operations
// permitted by x.md §15.1 #8 and DEC-092.
func IsAllowedOperation(opName string) bool {
	return allowedGraphQLOperations[opName]
}

type graphqlRequest struct {
	operation       string
	variables       map[string]any
	fieldToggles    map[string]bool
	referer         string
	guestNoCookie   bool
	candidate       *candidateCookieCarrier
	skipStoreExpire bool
}

type graphqlResponse struct {
	data json.RawMessage
	raw  *apiRawResponse
}

func shouldTriggerGraphQLRefresh(statusCode int, body []byte) (string, bool) {
	if reason, ok := ShouldTriggerFrontendRefresh(statusCode, body); ok {
		return reason, true
	}
	if statusCode == http.StatusGone && len(bytes.TrimSpace(body)) == 0 {
		return "empty_410", true
	}
	return "", false
}

func (c *Client) ensureMetadataForRequest(ctx context.Context, candidate *candidateCookieCarrier) (*FrontendMetadata, error) {
	if c.discovery == nil {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindSchemaChanged,
			Message: "X 前端元数据发现器未初始化",
		}
	}
	callCtx := ctx
	if candidate != nil {
		callCtx = withCandidateCookies(ctx, candidate)
	}
	meta, err := c.discovery.Ensure(callCtx)
	if err != nil {
		c.ReportAutomaticError(ctx, err)
		return nil, err
	}
	return meta, nil
}

func (c *Client) refreshMetadataForRequest(ctx context.Context, opName, reason string, candidate *candidateCookieCarrier) (*FrontendMetadata, error) {
	if c.discovery == nil {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindSchemaChanged,
			Message: "X 前端元数据发现器未初始化",
		}
	}
	callCtx := ctx
	if candidate != nil {
		callCtx = withCandidateCookies(ctx, candidate)
	}
	meta, err := c.discovery.Refresh(callCtx, reason)
	if err != nil {
		if !IsOptionalOperation(opName) {
			c.ReportAutomaticError(ctx, err)
		}
		return nil, err
	}
	return meta, nil
}

func (c *Client) ensureOptionalOperationForRequest(
	ctx context.Context,
	opName string,
	candidate *candidateCookieCarrier,
) (*FrontendMetadata, GraphQLOperationSpec, error) {
	if c.discovery == nil {
		return nil, GraphQLOperationSpec{}, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindSchemaChanged,
			Message: "X 前端元数据发现器未初始化",
		}
	}
	callCtx := ctx
	if candidate != nil {
		callCtx = withCandidateCookies(ctx, candidate)
	}
	meta, spec, err := c.discovery.EnsureOperation(callCtx, opName)
	if err != nil {
		// Deliberately do NOT trip X's site-wide automatic circuit breaker when only
		// an optional Mine operation (Bookmarks/Likes) lazy chunk is unavailable.
		var se *site.Error
		if errors.As(err, &se) && se != nil && se.Endpoint == "" {
			cp := *se
			cp.Endpoint = site.SanitizeEndpoint(c.endpoints.GraphQLBase + "/" + opName)
			return nil, GraphQLOperationSpec{}, &cp
		}
		return nil, GraphQLOperationSpec{}, err
	}
	return meta, spec, nil
}

// doGraphQL executes one of the 7 allowed read-only GraphQL GET queries with:
//   - strict operation allowlist (x.md §15.1 #8)
//   - per-operation rate-limit brake check (0 upstream calls when remaining <= 1)
//   - runtime frontend metadata (Bearer, queryId, features, fieldToggles, txid)
//   - x-client-transaction-id attached on ALL GraphQL requests
//   - passive ct0 sync & at-most-once 353 replay
//   - at-most-once cooled-down FrontendDiscovery.Refresh + replay on schema drift signals
//   - ZERO other automatic retries
func (c *Client) doGraphQL(ctx context.Context, req graphqlRequest) (*graphqlResponse, error) {
	if !IsAllowedOperation(req.operation) {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindBadRequest,
			Message: fmt.Sprintf("不允许调用未在白名单内的 X GraphQL 操作: %s", req.operation),
		}
	}

	if !req.guestNoCookie && req.candidate == nil {
		if err := c.RequireAuth(c.endpoints.GraphQLBase + "/" + req.operation); err != nil {
			return nil, err
		}
	}

	// Check per-operation rate-limit brake BEFORE triggering frontend discovery or any HTTP call.
	if err := c.checkOperationRateLimit(ctx, req.operation, c.endpoints.GraphQLBase+"/"+req.operation); err != nil {
		return nil, err
	}

	meta, err := c.ensureMetadataForRequest(ctx, req.candidate)
	if err != nil {
		return nil, err
	}

	refreshedOnce := false
	var missingNullFeatures []string
	for {
		spec, ok := meta.Operation(req.operation)
		if !ok || spec.QueryID == "" {
			if IsOptionalOperation(req.operation) {
				resolvedMeta, resolvedSpec, lErr := c.ensureOptionalOperationForRequest(ctx, req.operation, req.candidate)
				if lErr != nil {
					return nil, lErr
				}
				meta = resolvedMeta
				spec = resolvedSpec
			} else {
				se := &site.Error{
					Site:    SiteID,
					Kind:    site.ErrKindSchemaChanged,
					Message: fmt.Sprintf("X 前端元数据缺少操作 %s 的定义，已暂停 X 请求", req.operation),
				}
				c.ReportAutomaticError(ctx, se)
				return nil, se
			}
		}
		if len(missingNullFeatures) > 0 {
			extraFeats := make(map[string]bool)
			if spec.Features == nil {
				spec.Features = make(map[string]bool, len(missingNullFeatures))
			}
			for _, flagKey := range missingNullFeatures {
				if _, exists := spec.Features[flagKey]; exists {
					continue
				}
				val, hasDefault := meta.FeatureDefault(flagKey)
				if !hasDefault && flagKey == "graphql_timeline_v2_bookmark_timeline" {
					val = true
				}
				spec.Features[flagKey] = val
				extraFeats[flagKey] = val
			}
			if len(extraFeats) > 0 && c.discovery != nil {
				if updated := c.discovery.MergeOperationFeatures(req.operation, extraFeats); updated != nil {
					meta = updated
				}
			}
			missingNullFeatures = nil
		}
		if meta.TxID == nil {
			se := &site.Error{
				Site:    SiteID,
				Kind:    site.ErrKindSchemaChanged,
				Message: "X 事务头生成器尚未初始化，已暂停 X 请求",
			}
			c.ReportAutomaticError(ctx, se)
			return nil, se
		}

		safeEndpoint := site.SanitizeEndpoint(c.endpoints.GraphQLBase + "/" + req.operation)
		endpoint, query, buildErr := c.buildGraphQLRequestURL(spec, req.variables, req.fieldToggles)
		if buildErr != nil {
			return nil, buildErr
		}

		txGen := meta.TxID
		rawResp, execErr := c.executeRawAPIRequest(ctx, apiRequestSpec{
			method:          http.MethodGet,
			operation:       req.operation,
			endpoint:        endpoint,
			query:           query,
			bearerHeader:    meta.BearerHeader(),
			referer:         req.referer,
			origin:          DefaultOrigin,
			guestNoCookie:   req.guestNoCookie,
			candidate:       req.candidate,
			skipStoreExpire: req.skipStoreExpire,
			beforeAttempt: func(httpReq *http.Request) error {
				txid, gErr := txGen.Generate(httpReq.Method, httpReq.URL.Path)
				if gErr != nil {
					return gErr
				}
				httpReq.Header.Set("x-client-transaction-id", txid)
				return nil
			},
		})
		if execErr != nil {
			var se *site.Error
			if errors.As(execErr, &se) && se != nil && spec.QueryID != "" && strings.Contains(se.Endpoint, spec.QueryID) {
				cp := *se
				cp.Endpoint = safeEndpoint
				return nil, &cp
			}
			return nil, execErr
		}

		// Drift signal: trigger cooled-down FrontendDiscovery.Refresh AT MOST ONCE and replay ONCE.
		if reason, drift := shouldTriggerGraphQLRefresh(rawResp.statusCode, rawResp.body); drift {
			if !refreshedOnce {
				if reason == "features_cannot_be_null" {
					missingNullFeatures = ExtractNullFeatureNames(rawResp.body)
				}
				refreshedMeta, rErr := c.refreshMetadataForRequest(ctx, req.operation, reason, req.candidate)
				if rErr != nil {
					// Preserve schema_changed (e.g. 10m cooldown) and stop X only.
					return nil, rErr
				}
				meta = refreshedMeta
				refreshedOnce = true
				continue
			}
			se := &site.Error{
				Site:       SiteID,
				Kind:       site.ErrKindSchemaChanged,
				HTTPStatus: rawResp.statusCode,
				Endpoint:   safeEndpoint,
				Message:    fmt.Sprintf("X 前端元数据刷新后操作 %s 仍失败（%s），已暂停 X 请求", req.operation, reason),
			}
			if !IsOptionalOperation(req.operation) {
				c.ReportAutomaticError(ctx, se)
			}
			return nil, se
		}

		if se := ClassifyHTTPAndEnvelopeError(EnvelopeErrorContext{
			Endpoint:                safeEndpoint,
			Operation:               req.operation,
			StatusCode:              rawResp.statusCode,
			Header:                  rawResp.header,
			Body:                    rawResp.body,
			HasCookie:               rawResp.hasCookie,
			Now:                     c.nowFn().UTC(),
			ConsecutiveHeaderless88: rawResp.consecutiveHeaderless88,
		}); se != nil {
			c.onClassifiedError(ctx, se, rawResp.usingStore && !req.skipStoreExpire)
			return nil, se
		}

		var env struct {
			Data json.RawMessage `json:"data"`
		}
		if err := decodeJSONUseNumber(rawResp.body, &env); err != nil {
			se := &site.Error{
				Site:       SiteID,
				Kind:       site.ErrKindSchemaChanged,
				HTTPStatus: rawResp.statusCode,
				Endpoint:   safeEndpoint,
				Message:    fmt.Sprintf("解析 X GraphQL %s 响应失败", req.operation),
				Err:        err,
			}
			c.ReportAutomaticError(ctx, se)
			return nil, se
		}
		if isJSONNull(env.Data) {
			se := &site.Error{
				Site:       SiteID,
				Kind:       site.ErrKindSchemaChanged,
				HTTPStatus: rawResp.statusCode,
				Endpoint:   safeEndpoint,
				Message:    fmt.Sprintf("X GraphQL %s 响应缺少 data 字段", req.operation),
			}
			c.ReportAutomaticError(ctx, se)
			return nil, se
		}

		return &graphqlResponse{
			data: env.Data,
			raw:  rawResp,
		}, nil
	}
}

func (c *Client) buildGraphQLRequestURL(
	spec GraphQLOperationSpec,
	variables map[string]any,
	extraFieldToggles map[string]bool,
) (string, url.Values, error) {
	if variables == nil {
		variables = map[string]any{}
	}
	varsBytes, err := json.Marshal(variables)
	if err != nil {
		return "", nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindBadRequest,
			Message: "序列化 X GraphQL variables 参数失败",
			Err:     err,
		}
	}
	feats := spec.Features
	if feats == nil {
		feats = map[string]bool{}
	}
	featBytes, err := json.Marshal(feats)
	if err != nil {
		return "", nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindBadRequest,
			Message: "序列化 X GraphQL features 参数失败",
			Err:     err,
		}
	}

	q := url.Values{}
	q.Set("variables", string(varsBytes))
	q.Set("features", string(featBytes))

	mergedToggles := make(map[string]bool, len(spec.FieldToggles)+len(extraFieldToggles))
	for k, v := range spec.FieldToggles {
		mergedToggles[k] = v
	}
	for k, v := range extraFieldToggles {
		mergedToggles[k] = v
	}
	if len(mergedToggles) > 0 {
		toggBytes, tErr := json.Marshal(mergedToggles)
		if tErr != nil {
			return "", nil, &site.Error{
				Site:    SiteID,
				Kind:    site.ErrKindBadRequest,
				Message: "序列化 X GraphQL fieldToggles 参数失败",
				Err:     tErr,
			}
		}
		q.Set("fieldToggles", string(toggBytes))
	}

	base := strings.TrimRight(strings.TrimSpace(c.endpoints.GraphQLBase), "/")
	endpoint := base + "/" + spec.QueryID + "/" + spec.OperationName
	return endpoint, q, nil
}
