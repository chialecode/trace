package x

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/chialecode/trace/internal/site"
)

const (
	// DefaultHomeURL is the X web entry page used for runtime frontend metadata discovery.
	DefaultHomeURL = "https://x.com/home"

	// StaticAssetHost is the sole allowed host for unauthenticated frontend JS bundles.
	StaticAssetHost = "abs.twimg.com"

	// StaticAssetPathPrefix is the sole allowed URL path prefix on StaticAssetHost.
	StaticAssetPathPrefix = "/responsive-web/"

	// MaxStaticAssetBytes caps each downloaded frontend resource at 8 MiB (x.md §15.1 #4).
	MaxStaticAssetBytes int64 = 8 << 20

	// DefaultFrontendRefreshCooldown limits drift-triggered re-discovery to once per 10 minutes.
	DefaultFrontendRefreshCooldown = 10 * time.Minute

	// DefaultFrontendMetadataTTL is the in-memory session cache lifetime for discovered metadata.
	DefaultFrontendMetadataTTL = 1 * time.Hour

	// DefaultStaticAssetTimeout is the per-request timeout for fetching static JS bundles.
	DefaultStaticAssetTimeout = 15 * time.Second

	maxStaticAssetRedirects   = 3
	maxLazyChunksPerOperation = 2
	maxLazyCandidatesPerOp    = 8
)

// Read-only GraphQL operation names used by TRACE for X (x.md §15.1 #8).
const (
	OpUserByScreenName = "UserByScreenName"
	OpUserByRestId     = "UserByRestId"
	OpUserTweets       = "UserTweets"
	OpTweetDetail      = "TweetDetail"
	OpFollowing        = "Following"
	OpBookmarks        = "Bookmarks"
	OpLikes            = "Likes"
)

// CoreOperations returns a fresh copy of the 5 core GraphQL operation names that
// MUST be extracted from main.*.js during runtime frontend metadata discovery.
// Login, user profiles, timelines, post details, caching, subscriptions, and Following
// depend only on these 5 core operations.
func CoreOperations() []string {
	return []string{
		OpUserByScreenName,
		OpUserByRestId,
		OpUserTweets,
		OpTweetDetail,
		OpFollowing,
	}
}

// OptionalOperations returns a fresh copy of the read-only "Mine" GraphQL operation
// names that X may code-split into lazy webpack/Vite chunks outside main.*.js.
// Their absence from main.*.js MUST NOT fail core frontend discovery or login; they
// are resolved lazily on demand via FrontendDiscovery.EnsureOperation.
func OptionalOperations() []string {
	return []string{
		OpBookmarks,
		OpLikes,
	}
}

// IsOptionalOperation reports whether opName is an optional/lazy Mine GraphQL operation
// (Bookmarks or Likes) that may be code-split outside main.*.js.
func IsOptionalOperation(opName string) bool {
	return opName == OpBookmarks || opName == OpLikes
}

// RequiredOperations returns a fresh copy of all 7 read-only GraphQL operation names
// used by TRACE for X (5 core operations in main.*.js + 2 lazy/optional Mine operations).
func RequiredOperations() []string {
	return []string{
		OpUserByScreenName,
		OpUserByRestId,
		OpUserTweets,
		OpTweetDetail,
		OpFollowing,
		OpBookmarks,
		OpLikes,
	}
}

// FrontendRequestKind identifies which frontend discovery request is being executed.
type FrontendRequestKind string

const (
	FrontendRequestHomeHTML   FrontendRequestKind = "home_html"
	FrontendRequestMainBundle FrontendRequestKind = "main_js"
	FrontendRequestOndemandJS FrontendRequestKind = "ondemand_js"
	FrontendRequestLazyChunk  FrontendRequestKind = "lazy_chunk_js"
)

// HomeHTMLFetcher retrieves the authenticated X home HTML page (carrying session Cookies
// just like a logged-in browser).
type HomeHTMLFetcher func(ctx context.Context, homeURL string) ([]byte, error)

// RequestChargeFunc is an optional hook called before each of the 3 frontend discovery
// fetches (home HTML, main.*.js, ondemand.s.*.js) so the X client can charge them to
// X's serial gate and hourly quota.
type RequestChargeFunc func(ctx context.Context, kind FrontendRequestKind, targetURL string) error

// RequestAcquireFunc is an optional hook that acquires X's serial request gate / quota
// slot before each frontend discovery HTTP fetch and returns a release function invoked
// once that fetch's HTTP I/O completes (response body read and closed).
type RequestAcquireFunc func(ctx context.Context, kind FrontendRequestKind, targetURL string) (release func(), err error)

// GraphQLOperationSpec holds runtime-discovered metadata for one X GraphQL query.
type GraphQLOperationSpec struct {
	OperationName string          `json:"operationName"`
	QueryID       string          `json:"queryId"`
	Features      map[string]bool `json:"features"`
	FieldToggles  map[string]bool `json:"fieldToggles,omitempty"`
}

// FrontendMetadata holds runtime-discovered X web frontend metadata in memory only.
// All fields are excluded from JSON marshaling so Bearer tokens and frontend internals
// are never accidentally persisted or logged.
// Published FrontendMetadata instances are treated as immutable; lazy operation merges
// publish a cloned copy-on-write snapshot so concurrent readers remain race-free.
type FrontendMetadata struct {
	Bearer       string                          `json:"-"`
	Operations   map[string]GraphQLOperationSpec `json:"-"`
	TxIDMaterial TxIDMaterial                    `json:"-"`
	TxID         *TxIDGenerator                  `json:"-"`
	FetchedAt    time.Time                       `json:"-"`
	MainJSURL    string                          `json:"-"`
	OndemandURL  string                          `json:"-"`

	featureDefaults map[string]bool     `json:"-"`
	lazyChunkURLs   map[string][]string `json:"-"`
	fetchedLazyURLs map[string]bool     `json:"-"`
}

// FetchedLazyChunkURLs returns a sorted defensive copy of lazy chunk URLs fetched so far
// for this metadata generation (in-memory only).
func (m *FrontendMetadata) FetchedLazyChunkURLs() []string {
	if m == nil || len(m.fetchedLazyURLs) == 0 {
		return nil
	}
	out := make([]string, 0, len(m.fetchedLazyURLs))
	for u := range m.fetchedLazyURLs {
		out = append(out, u)
	}
	sort.Strings(out)
	return out
}

// LazyChunkCandidates returns a defensive copy of validated candidate lazy chunk URLs
// discovered for opName from home HTML / main.*.js.
func (m *FrontendMetadata) LazyChunkCandidates(opName string) []string {
	if m == nil || len(m.lazyChunkURLs) == 0 {
		return nil
	}
	list := m.lazyChunkURLs[opName]
	if len(list) == 0 {
		return nil
	}
	return append([]string(nil), list...)
}

func (m *FrontendMetadata) cloneWithLazyUpdates(
	newOps map[string]GraphQLOperationSpec,
	newlyFetched []string,
) *FrontendMetadata {
	if m == nil {
		return nil
	}
	ops := make(map[string]GraphQLOperationSpec, len(m.Operations)+len(newOps))
	for k, v := range m.Operations {
		ops[k] = v
	}
	for k, v := range newOps {
		ops[k] = v
	}
	fetched := make(map[string]bool, len(m.fetchedLazyURLs)+len(newlyFetched))
	for u := range m.fetchedLazyURLs {
		fetched[u] = true
	}
	for _, u := range newlyFetched {
		if u != "" {
			fetched[u] = true
		}
	}
	return &FrontendMetadata{
		Bearer:          m.Bearer,
		Operations:      ops,
		TxIDMaterial:    m.TxIDMaterial,
		TxID:            m.TxID,
		FetchedAt:       m.FetchedAt,
		MainJSURL:       m.MainJSURL,
		OndemandURL:     m.OndemandURL,
		featureDefaults: m.featureDefaults,
		lazyChunkURLs:   m.lazyChunkURLs,
		fetchedLazyURLs: fetched,
	}
}

// String returns a redacted summary that never exposes the Bearer token.
func (m *FrontendMetadata) String() string {
	if m == nil {
		return "FrontendMetadata<nil>"
	}
	return fmt.Sprintf("FrontendMetadata{ops=%d fetchedAt=%s}", len(m.Operations), m.FetchedAt.UTC().Format(time.RFC3339))
}

// BearerHeader returns the formatted "Bearer <token>" Authorization header value.
func (m *FrontendMetadata) BearerHeader() string {
	if m == nil || m.Bearer == "" {
		return ""
	}
	return "Bearer " + m.Bearer
}

// Operation returns a defensive copy of the discovered GraphQLOperationSpec for name.
func (m *FrontendMetadata) Operation(name string) (GraphQLOperationSpec, bool) {
	if m == nil || len(m.Operations) == 0 {
		return GraphQLOperationSpec{}, false
	}
	spec, ok := m.Operations[name]
	if !ok {
		return GraphQLOperationSpec{}, false
	}
	out := GraphQLOperationSpec{
		OperationName: spec.OperationName,
		QueryID:       spec.QueryID,
	}
	if spec.Features != nil {
		out.Features = make(map[string]bool, len(spec.Features))
		for k, v := range spec.Features {
			out.Features[k] = v
		}
	}
	if spec.FieldToggles != nil {
		out.FieldToggles = make(map[string]bool, len(spec.FieldToggles))
		for k, v := range spec.FieldToggles {
			out.FieldToggles[k] = v
		}
	}
	return out, true
}

// ParsedHomeHTML holds the metadata extracted from the X home HTML document.
type ParsedHomeHTML struct {
	VerificationKey string
	SVGPaths        []string
	MainJSURL       string
	OndemandJSURL   string
	LazyChunkURLs   map[string][]string
}

// FrontendDiscoveryConfig configures runtime frontend metadata discovery.
type FrontendDiscoveryConfig struct {
	HomeURL              string
	FetchHome            HomeHTMLFetcher
	ChargeRequest        RequestChargeFunc
	AcquireRequest       RequestAcquireFunc
	UserAgent            string
	Now                  func() time.Time
	RefreshCooldown      time.Duration
	MetadataTTL          time.Duration
	AssetTimeout         time.Duration
	MaxAssetBytes        int64
	AllowLocalhostAssets bool
	AssetTransport       http.RoundTripper
	TxIDOptions          []TxIDOption
}

type discoveryFlight struct {
	done    chan struct{}
	meta    *FrontendMetadata
	err     error
	cancel  context.CancelFunc
	waiters int
}

type opDiscoveryFlight struct {
	done    chan struct{}
	meta    *FrontendMetadata
	spec    GraphQLOperationSpec
	err     error
	cancel  context.CancelFunc
	waiters int
}

// FrontendDiscovery discovers and caches X web frontend metadata (public Bearer token,
// GraphQL queryIds/features/fieldToggles, and x-client-transaction-id material) in memory
// with singleflight deduplication and a 10-minute drift-refresh cooldown.
type FrontendDiscovery struct {
	homeURL         string
	fetchHome       HomeHTMLFetcher
	chargeRequest   RequestChargeFunc
	acquireRequest  RequestAcquireFunc
	userAgent       string
	now             func() time.Time
	refreshCooldown time.Duration
	metadataTTL     time.Duration
	assetTimeout    time.Duration
	maxAssetBytes   int64
	allowLocalhost  bool
	assetClient     *http.Client
	txIDOptions     []TxIDOption

	mu            sync.Mutex
	current       *FrontendMetadata
	lastRefreshAt time.Time
	inflight      *discoveryFlight
	opFlights     map[string]*opDiscoveryFlight
	lastOpMissAt  map[string]time.Time
}

// NewFrontendDiscovery builds a FrontendDiscovery instance with dedicated
// unauthenticated static asset fetching and strict host/path/size guards.
func NewFrontendDiscovery(cfg FrontendDiscoveryConfig) *FrontendDiscovery {
	homeURL := strings.TrimSpace(cfg.HomeURL)
	if homeURL == "" {
		homeURL = DefaultHomeURL
	}
	ua := strings.TrimSpace(cfg.UserAgent)
	if ua == "" {
		ua = site.DefaultUserAgent
	}
	nowFn := cfg.Now
	if nowFn == nil {
		nowFn = time.Now
	}
	cooldown := cfg.RefreshCooldown
	if cooldown <= 0 {
		cooldown = DefaultFrontendRefreshCooldown
	}
	ttl := cfg.MetadataTTL
	if ttl <= 0 {
		ttl = DefaultFrontendMetadataTTL
	}
	timeout := cfg.AssetTimeout
	if timeout <= 0 {
		timeout = DefaultStaticAssetTimeout
	}
	maxBytes := cfg.MaxAssetBytes
	if maxBytes <= 0 {
		maxBytes = MaxStaticAssetBytes
	}

	transport := cfg.AssetTransport
	if transport == nil {
		guarded := site.NewGuardedMediaDialer(
			SiteID,
			cfg.AllowLocalhostAssets,
			&net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second},
			"已拦截不安全的 X 前端资源地址",
		)
		transport = &http.Transport{
			Proxy:                 guarded.Proxy,
			DialContext:           guarded.DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          4,
			IdleConnTimeout:       60 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: timeout,
		}
	}

	d := &FrontendDiscovery{
		homeURL:         homeURL,
		fetchHome:       cfg.FetchHome,
		chargeRequest:   cfg.ChargeRequest,
		acquireRequest:  cfg.AcquireRequest,
		userAgent:       ua,
		now:             nowFn,
		refreshCooldown: cooldown,
		metadataTTL:     ttl,
		assetTimeout:    timeout,
		maxAssetBytes:   maxBytes,
		allowLocalhost:  cfg.AllowLocalhostAssets,
		txIDOptions:     append([]TxIDOption(nil), cfg.TxIDOptions...),
		opFlights:       make(map[string]*opDiscoveryFlight),
		lastOpMissAt:    make(map[string]time.Time),
	}

	d.assetClient = &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxStaticAssetRedirects {
				return &site.Error{
					Site:    SiteID,
					Kind:    site.ErrKindBlocked,
					Message: "X 前端资源重定向次数过多",
				}
			}
			if _, err := ValidateStaticAssetURL(req.URL.String(), d.allowLocalhost); err != nil {
				return err
			}
			stripStaticAuthHeaders(req)
			return nil
		},
	}

	return d
}

// Current returns the currently cached in-memory metadata without performing any network calls.
func (d *FrontendDiscovery) Current() *FrontendMetadata {
	if d == nil {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.current
}

// Ensure returns cached in-memory frontend metadata if present and fresh, or triggers
// a singleflight runtime discovery run. Concurrent callers share one upstream run.
func (d *FrontendDiscovery) Ensure(ctx context.Context) (*FrontendMetadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	d.mu.Lock()
	if d.current != nil && d.now().Sub(d.current.FetchedAt) < d.metadataTTL {
		meta := d.current
		d.mu.Unlock()
		return meta, nil
	}
	if d.inflight != nil {
		f := d.inflight
		f.waiters++
		d.mu.Unlock()
		return d.waitFlight(ctx, f)
	}
	f := d.startFlightLocked(ctx, false)
	d.mu.Unlock()
	return d.waitFlight(ctx, f)
}

// Refresh forces a runtime re-discovery in response to schema drift signals (such as
// empty-body 404, 400 features cannot be null, or Query: Unspecified). Concurrent callers
// deduplicate via singleflight; a second refresh within RefreshCooldown (default 10m)
// performs 0 upstream calls and returns site.ErrKindSchemaChanged.
func (d *FrontendDiscovery) Refresh(ctx context.Context, reason string) (*FrontendMetadata, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	d.mu.Lock()
	if d.inflight != nil {
		f := d.inflight
		f.waiters++
		d.mu.Unlock()
		return d.waitFlight(ctx, f)
	}
	now := d.now()
	if !d.lastRefreshAt.IsZero() && now.Sub(d.lastRefreshAt) < d.refreshCooldown {
		d.mu.Unlock()
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindSchemaChanged,
			Message: "X 前端接口结构已变化，冷却期内不再重复发现，已暂停 X 请求",
		}
	}
	d.lastRefreshAt = now
	f := d.startFlightLocked(ctx, true)
	d.mu.Unlock()
	return d.waitFlight(ctx, f)
}

func (d *FrontendDiscovery) startFlightLocked(ctx context.Context, isRefresh bool) *discoveryFlight {
	flightTimeout := d.assetTimeout*3 + 15*time.Second
	if flightTimeout < 30*time.Second {
		flightTimeout = 30 * time.Second
	}
	baseCtx := context.Background()
	if ctx != nil {
		baseCtx = context.WithoutCancel(ctx)
	}
	flightCtx, cancel := context.WithTimeout(baseCtx, flightTimeout)
	f := &discoveryFlight{
		done:    make(chan struct{}),
		cancel:  cancel,
		waiters: 1,
	}
	d.inflight = f

	go func() {
		defer cancel()
		meta, err := d.discover(flightCtx)

		d.mu.Lock()
		f.meta = meta
		f.err = err
		if err == nil && meta != nil {
			d.current = meta
			d.lastOpMissAt = make(map[string]time.Time)
		} else if isRefresh && site.IsKind(err, site.ErrKindSchemaChanged) {
			d.current = nil
			d.lastOpMissAt = make(map[string]time.Time)
		}
		if d.inflight == f {
			d.inflight = nil
		}
		close(f.done)
		d.mu.Unlock()
	}()

	return f
}

func (d *FrontendDiscovery) waitFlight(ctx context.Context, f *discoveryFlight) (*FrontendMetadata, error) {
	select {
	case <-f.done:
		return f.meta, f.err
	case <-ctx.Done():
		d.mu.Lock()
		f.waiters--
		if f.waiters <= 0 {
			f.cancel()
		}
		d.mu.Unlock()
		return nil, ctx.Err()
	}
}

// EnsureOperation returns the GraphQLOperationSpec for opName, lazily fetching at most
// 1–2 allowlisted webpack/Vite candidate chunks on demand when opName is an optional
// Mine operation (Bookmarks or Likes) that was code-split outside main.*.js.
// Concurrent callers for the same opName deduplicate via singleflight; repeated misses
// within RefreshCooldown perform 0 upstream fetches and return site.ErrKindSchemaChanged
// only for opName without invalidating core metadata or affecting the 5 core operations.
func (d *FrontendDiscovery) EnsureOperation(ctx context.Context, opName string) (*FrontendMetadata, GraphQLOperationSpec, error) {
	if err := ctx.Err(); err != nil {
		return nil, GraphQLOperationSpec{}, err
	}
	meta, err := d.Ensure(ctx)
	if err != nil {
		return nil, GraphQLOperationSpec{}, err
	}
	if spec, ok := meta.Operation(opName); ok && spec.QueryID != "" {
		return meta, spec, nil
	}
	if !IsOptionalOperation(opName) {
		return nil, GraphQLOperationSpec{}, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindSchemaChanged,
			Message: fmt.Sprintf("X 前端元数据缺少核心操作 %s 的定义", opName),
		}
	}

	d.mu.Lock()
	if d.current != nil {
		if spec, ok := d.current.Operation(opName); ok && spec.QueryID != "" {
			cur := d.current
			d.mu.Unlock()
			return cur, spec, nil
		}
		meta = d.current
	}
	if d.opFlights == nil {
		d.opFlights = make(map[string]*opDiscoveryFlight)
	}
	if d.lastOpMissAt == nil {
		d.lastOpMissAt = make(map[string]time.Time)
	}
	if f, ok := d.opFlights[opName]; ok && f != nil {
		f.waiters++
		d.mu.Unlock()
		return d.waitOpFlight(ctx, f)
	}
	now := d.now()
	if lastMiss, ok := d.lastOpMissAt[opName]; ok && !lastMiss.IsZero() && now.Sub(lastMiss) < d.refreshCooldown {
		d.mu.Unlock()
		return nil, GraphQLOperationSpec{}, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindSchemaChanged,
			Message: fmt.Sprintf("X 前端延迟分片缺少操作 %s 的 queryId，冷却期内不再重复发现（核心功能不受影响）", opName),
		}
	}
	f := d.startOpFlightLocked(ctx, opName, meta)
	d.mu.Unlock()
	return d.waitOpFlight(ctx, f)
}

func (d *FrontendDiscovery) startOpFlightLocked(ctx context.Context, opName string, baseMeta *FrontendMetadata) *opDiscoveryFlight {
	flightTimeout := d.assetTimeout*maxLazyChunksPerOperation + 10*time.Second
	if flightTimeout < 30*time.Second {
		flightTimeout = 30 * time.Second
	}
	baseCtx := context.Background()
	if ctx != nil {
		baseCtx = context.WithoutCancel(ctx)
	}
	flightCtx, cancel := context.WithTimeout(baseCtx, flightTimeout)
	f := &opDiscoveryFlight{
		done:    make(chan struct{}),
		cancel:  cancel,
		waiters: 1,
	}
	d.opFlights[opName] = f

	go func() {
		defer cancel()
		discoveredOps, fetchedURLs, hardErr := d.resolveLazyOperation(flightCtx, opName, baseMeta)

		d.mu.Lock()
		defer func() {
			if d.opFlights[opName] == f {
				delete(d.opFlights, opName)
			}
			close(f.done)
			d.mu.Unlock()
		}()

		// Publish any discovered operations and fetched chunk URLs into the current metadata
		// snapshot via copy-on-write when d.current still belongs to the same main.*.js bundle.
		targetMeta := baseMeta
		if d.current != nil && baseMeta != nil &&
			d.current.MainJSURL == baseMeta.MainJSURL &&
			d.current.FetchedAt.Equal(baseMeta.FetchedAt) {
			targetMeta = d.current
			if len(discoveredOps) > 0 || len(fetchedURLs) > 0 {
				updated := d.current.cloneWithLazyUpdates(discoveredOps, fetchedURLs)
				d.current = updated
				targetMeta = updated
				for foundOp := range discoveredOps {
					delete(d.lastOpMissAt, foundOp)
				}
			}
		} else if d.current != nil {
			targetMeta = d.current
		}

		if hardErr != nil {
			f.err = hardErr
			return
		}

		if targetMeta != nil {
			if spec, ok := targetMeta.Operation(opName); ok && spec.QueryID != "" {
				f.meta = targetMeta
				f.spec = spec
				return
			}
		}

		d.lastOpMissAt[opName] = d.now()
		f.err = &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindSchemaChanged,
			Message: fmt.Sprintf("X 前端分片缺少可选操作 %s 的 queryId，其他核心功能不受影响", opName),
		}
	}()

	return f
}

func (d *FrontendDiscovery) waitOpFlight(ctx context.Context, f *opDiscoveryFlight) (*FrontendMetadata, GraphQLOperationSpec, error) {
	select {
	case <-f.done:
		return f.meta, f.spec, f.err
	case <-ctx.Done():
		d.mu.Lock()
		f.waiters--
		if f.waiters <= 0 {
			f.cancel()
		}
		d.mu.Unlock()
		return nil, GraphQLOperationSpec{}, ctx.Err()
	}
}

func (d *FrontendDiscovery) resolveLazyOperation(
	ctx context.Context,
	opName string,
	baseMeta *FrontendMetadata,
) (map[string]GraphQLOperationSpec, []string, error) {
	if baseMeta == nil {
		return nil, nil, nil
	}
	candidates := baseMeta.LazyChunkCandidates(opName)
	if len(candidates) == 0 {
		return nil, nil, nil
	}

	discoveredOps := make(map[string]GraphQLOperationSpec)
	var fetchedURLs []string
	fetchedCount := 0

	for _, rawURL := range candidates {
		if fetchedCount >= maxLazyChunksPerOperation {
			break
		}
		u, err := ValidateStaticAssetURL(rawURL, d.allowLocalhost)
		if err != nil {
			continue
		}
		cleanURL := u.String()
		if baseMeta.fetchedLazyURLs != nil && baseMeta.fetchedLazyURLs[cleanURL] {
			continue
		}
		fetchedCount++
		body, fetchErr := d.runCharged(ctx, FrontendRequestLazyChunk, cleanURL, func() ([]byte, error) {
			return d.fetchStaticAsset(ctx, cleanURL)
		})
		if fetchErr != nil {
			sanitizedErr := sanitizeFrontendError(fetchErr, baseMeta.Bearer)
			if errors.Is(sanitizedErr, context.Canceled) ||
				errors.Is(sanitizedErr, context.DeadlineExceeded) ||
				site.IsKind(sanitizedErr, site.ErrKindQuota) ||
				site.IsKind(sanitizedErr, site.ErrKindBlocked) {
				return discoveredOps, fetchedURLs, sanitizedErr
			}
			fetchedURLs = append(fetchedURLs, cleanURL)
			continue
		}
		fetchedURLs = append(fetchedURLs, cleanURL)

		chunkSpecs := parseLazyChunkOperations(string(body), baseMeta.featureDefaults)
		for k, spec := range chunkSpecs {
			if _, already := baseMeta.Operations[k]; !already {
				discoveredOps[k] = spec
			}
		}
		if spec, ok := discoveredOps[opName]; ok && spec.QueryID != "" {
			break
		}
	}

	return discoveredOps, fetchedURLs, nil
}

// ShouldTriggerFrontendRefresh reports whether an X GraphQL error response indicates
// frontend queryId/features drift that warrants a single cooled-down Refresh (x.md §15.1 #4).
func ShouldTriggerFrontendRefresh(httpStatus int, body []byte) (string, bool) {
	trimmed := bytes.TrimSpace(body)
	if httpStatus == http.StatusNotFound && len(trimmed) == 0 {
		return "empty_404", true
	}
	if httpStatus == http.StatusBadRequest {
		lower := strings.ToLower(string(trimmed))
		if strings.Contains(lower, "features cannot be null") {
			return "features_cannot_be_null", true
		}
		if strings.Contains(lower, "query: unspecified") {
			return "query_unspecified", true
		}
	}
	return "", false
}

func (d *FrontendDiscovery) discover(ctx context.Context) (*FrontendMetadata, error) {
	if d.fetchHome == nil {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindSchemaChanged,
			Message: "X 前端元数据发现缺少首页拉取器",
		}
	}

	homeBody, err := d.runCharged(ctx, FrontendRequestHomeHTML, d.homeURL, func() ([]byte, error) {
		return d.fetchHome(ctx, d.homeURL)
	})
	if err != nil {
		return nil, sanitizeFrontendError(err, "")
	}
	if int64(len(homeBody)) > d.maxAssetBytes {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindSchemaChanged,
			Message: "X 首页 HTML 体积超过上限",
		}
	}

	parsedHome, err := ParseHomeHTML(string(homeBody))
	if err != nil {
		return nil, err
	}

	mainURL, err := ValidateStaticAssetURL(parsedHome.MainJSURL, d.allowLocalhost)
	if err != nil {
		return nil, err
	}
	mainBody, err := d.runCharged(ctx, FrontendRequestMainBundle, mainURL.String(), func() ([]byte, error) {
		return d.fetchStaticAsset(ctx, mainURL.String())
	})
	if err != nil {
		return nil, err
	}

	bearer, ops, err := parseMainBundle(string(mainBody), string(homeBody))
	if err != nil {
		return nil, sanitizeFrontendError(err, bearer)
	}

	ondemandRawURL := parsedHome.OndemandJSURL
	if ondemandRawURL == "" {
		ondemandRawURL = extractOndemandURLFromMainJS(string(mainBody), mainURL)
	}
	if ondemandRawURL == "" {
		return nil, sanitizeFrontendError(&site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindSchemaChanged,
			Message: "X 前端资源缺少 ondemand.s 脚本地址",
		}, bearer)
	}

	ondemandURL, err := ValidateStaticAssetURL(ondemandRawURL, d.allowLocalhost)
	if err != nil {
		return nil, sanitizeFrontendError(err, bearer)
	}
	ondemandBody, err := d.runCharged(ctx, FrontendRequestOndemandJS, ondemandURL.String(), func() ([]byte, error) {
		return d.fetchStaticAsset(ctx, ondemandURL.String())
	})
	if err != nil {
		return nil, sanitizeFrontendError(err, bearer)
	}

	rowIndex, keyByteIndices, err := ParseOndemandIndices(string(ondemandBody))
	if err != nil {
		return nil, sanitizeFrontendError(err, bearer)
	}

	mat := TxIDMaterial{
		VerificationKey: parsedHome.VerificationKey,
		SVGPaths:        parsedHome.SVGPaths,
		RowIndex:        rowIndex,
		KeyByteIndices:  keyByteIndices,
	}
	txGen, err := NewTxIDGenerator(mat, d.txIDOptions...)
	if err != nil {
		return nil, sanitizeFrontendError(err, bearer)
	}

	featureDefaults := scanAllFeatureDefaults(string(mainBody), string(homeBody))
	lazyURLs := filterAndMergeLazyChunkURLs(
		d.allowLocalhost,
		parsedHome.LazyChunkURLs,
		extractLazyChunkURLsFromMainJS(string(mainBody), mainURL),
	)

	return &FrontendMetadata{
		Bearer:          bearer,
		Operations:      ops,
		TxIDMaterial:    mat,
		TxID:            txGen,
		FetchedAt:       d.now(),
		MainJSURL:       mainURL.String(),
		OndemandURL:     ondemandURL.String(),
		featureDefaults: featureDefaults,
		lazyChunkURLs:   lazyURLs,
		fetchedLazyURLs: make(map[string]bool),
	}, nil
}

func (d *FrontendDiscovery) charge(ctx context.Context, kind FrontendRequestKind, targetURL string) (func(), error) {
	sanitized := site.SanitizeEndpoint(targetURL)
	var release func()
	if d.acquireRequest != nil {
		var err error
		release, err = d.acquireRequest(ctx, kind, sanitized)
		if err != nil {
			return nil, err
		}
	}
	if d.chargeRequest != nil {
		if err := d.chargeRequest(ctx, kind, sanitized); err != nil {
			if release != nil {
				release()
			}
			return nil, err
		}
	}
	return release, nil
}

func (d *FrontendDiscovery) runCharged(ctx context.Context, kind FrontendRequestKind, targetURL string, fn func() ([]byte, error)) ([]byte, error) {
	release, err := d.charge(ctx, kind, targetURL)
	if err != nil {
		return nil, err
	}
	if release != nil {
		defer release()
	}
	return fn()
}

func (d *FrontendDiscovery) fetchStaticAsset(ctx context.Context, rawURL string) ([]byte, error) {
	u, err := ValidateStaticAssetURL(rawURL, d.allowLocalhost)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindBadRequest,
			Message: "无法构造 X 前端资源请求",
		}
	}
	stripStaticAuthHeaders(req)
	req.Header.Set("User-Agent", d.userAgent)
	req.Header.Set("Accept", "*/*")

	resp, err := d.assetClient.Do(req)
	if err != nil {
		var se *site.Error
		if errors.As(err, &se) && se != nil {
			return nil, se
		}
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindNetwork,
			Message: "拉取 X 前端静态脚本失败",
			Err:     site.UnwrapURLError(err),
		}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, &site.Error{
			Site:       SiteID,
			Kind:       site.ErrKindSchemaChanged,
			HTTPStatus: resp.StatusCode,
			Endpoint:   site.SanitizeEndpoint(u.String()),
			Message:    fmt.Sprintf("拉取 X 前端静态脚本返回 HTTP %d", resp.StatusCode),
		}
	}
	if resp.ContentLength > d.maxAssetBytes {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: site.SanitizeEndpoint(u.String()),
			Message:  "X 前端静态脚本体积超过 8 MiB 上限",
		}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, d.maxAssetBytes+1))
	if err != nil {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindNetwork,
			Message: "读取 X 前端静态脚本失败",
			Err:     site.UnwrapURLError(err),
		}
	}
	if int64(len(body)) > d.maxAssetBytes {
		return nil, &site.Error{
			Site:     SiteID,
			Kind:     site.ErrKindSchemaChanged,
			Endpoint: site.SanitizeEndpoint(u.String()),
			Message:  "X 前端静态脚本体积超过 8 MiB 上限",
		}
	}
	return body, nil
}

func stripStaticAuthHeaders(req *http.Request) {
	if req == nil {
		return
	}
	req.Header.Del("Cookie")
	req.Header.Del("Authorization")
	req.Header.Del("X-Csrf-Token")
	req.Header.Del("Referer")
	req.Header.Del("Origin")
}

// ValidateStaticAssetURL enforces x.md §15.1 #4 static bundle rules:
// HTTPS on port 443, exact host abs.twimg.com, path strictly under /responsive-web/
// without dot-segment traversal, and no userinfo. When allowLocalhost is true (for tests),
// loopback HTTP/HTTPS hosts are also accepted while keeping the /responsive-web/ path rule.
func ValidateStaticAssetURL(rawURL string, allowLocalhost bool) (*url.URL, error) {
	trimmed := strings.TrimSpace(rawURL)
	if trimmed == "" || strings.ContainsAny(trimmed, "\r\n\\") {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindBlocked,
			Message: "X 前端资源地址无效",
		}
	}
	u, err := url.Parse(trimmed)
	if err != nil || u.Host == "" || u.User != nil {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindBlocked,
			Message: "X 前端资源地址格式不合规",
		}
	}

	host := strings.ToLower(u.Hostname())
	scheme := strings.ToLower(u.Scheme)
	isAllowedLoopback := allowLocalhost && (host == "127.0.0.1" || host == "localhost" || host == "::1")

	if isAllowedLoopback {
		if scheme != "http" && scheme != "https" {
			return nil, &site.Error{
				Site:    SiteID,
				Kind:    site.ErrKindBlocked,
				Message: "已拦截非 HTTP(S) 的 X 前端资源地址",
			}
		}
	} else {
		if scheme != "https" {
			return nil, &site.Error{
				Site:    SiteID,
				Kind:    site.ErrKindBlocked,
				Message: "已拦截非 HTTPS 的 X 前端资源地址",
			}
		}
		if port := u.Port(); port != "" && port != "443" {
			return nil, &site.Error{
				Site:    SiteID,
				Kind:    site.ErrKindBlocked,
				Message: "已拦截非常规端口的 X 前端资源地址",
			}
		}
		if host != StaticAssetHost {
			return nil, &site.Error{
				Site:    SiteID,
				Kind:    site.ErrKindBlocked,
				Message: "已拦截非 abs.twimg.com 的 X 前端资源主机",
			}
		}
	}

	escaped := u.EscapedPath()
	lowerEscaped := strings.ToLower(escaped)
	if !strings.HasPrefix(u.Path, StaticAssetPathPrefix) ||
		!strings.HasPrefix(escaped, StaticAssetPathPrefix) ||
		strings.Contains(u.Path, "..") ||
		strings.Contains(lowerEscaped, "%2e") ||
		strings.Contains(lowerEscaped, "%2f") ||
		strings.Contains(lowerEscaped, "%5c") {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindBlocked,
			Message: "已拦截不在 /responsive-web/ 路径下的 X 前端资源地址",
		}
	}
	cleaned := path.Clean(u.Path)
	if !strings.HasPrefix(cleaned, StaticAssetPathPrefix) {
		return nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindBlocked,
			Message: "已拦截越出 /responsive-web/ 路径前缀的 X 前端资源地址",
		}
	}
	return u, nil
}

func sanitizeFrontendError(err error, bearer string) error {
	if err == nil {
		return nil
	}
	err = site.UnwrapURLError(err)
	if bearer == "" {
		return err
	}
	if !strings.Contains(err.Error(), bearer) {
		return err
	}
	var se *site.Error
	if errors.As(err, &se) && se != nil {
		cp := *se
		cp.Message = strings.ReplaceAll(cp.Message, bearer, "<redacted>")
		cp.Endpoint = strings.ReplaceAll(cp.Endpoint, bearer, "<redacted>")
		cp.Err = nil
		return &cp
	}
	return errors.New(strings.ReplaceAll(err.Error(), bearer, "<redacted>"))
}

// ---------- HTML Parsing (Webpack + Vite) ----------

var (
	reMetaTag      = regexp.MustCompile(`(?is)<meta\b[^>]*>`)
	reScriptTag    = regexp.MustCompile(`(?is)<script\b[^>]*>`)
	reLinkTag      = regexp.MustCompile(`(?is)<link\b[^>]*>`)
	reSVGBlock     = regexp.MustCompile(`(?is)<svg\b([^>]*)>(.*?)</svg>`)
	rePathTag      = regexp.MustCompile(`(?is)<path\b[^>]*>`)
	reHTMLAttr     = regexp.MustCompile(`(?is)([A-Za-z_:][A-Za-z0-9_.:-]*)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>` + "`" + `]+))`)
	reAnimID       = regexp.MustCompile(`(?i)^loading-x-anim-([0-3])$`)
	reMainFile     = regexp.MustCompile(`^main[.-][A-Za-z0-9_-]+\.js$`)
	reOndemandFile = regexp.MustCompile(`^ondemand\.s[.-][A-Za-z0-9_-]+\.js$`)

	reWebpackOndemandDirect = regexp.MustCompile(`["']ondemand\.s["']\s*:\s*["']([A-Za-z0-9_-]{4,64})["']`)
	reWebpackOndemandNumID  = regexp.MustCompile(`(?:\b|["'])(\d+)["']?\s*:\s*["']ondemand\.s["']`)
	reWebpackChunkNumID     = regexp.MustCompile(`(?:\b|["'])(\d+)["']?\s*:\s*["']([A-Za-z0-9_.~-]{3,160})["']`)
	reWebpackChunkDirect    = regexp.MustCompile(`["']([A-Za-z0-9_.~-]{3,160})["']\s*:\s*["']([A-Za-z0-9_-]{4,32})["']`)
	reWebpackHexHash        = regexp.MustCompile(`^[0-9a-f]{6,16}$`)
	reScriptCloseTag        = regexp.MustCompile(`(?i)</script\b`)
	reNoModuleAttr          = regexp.MustCompile(`(?i)\snomodule(?:\s*=|\s|>|/)`)
	reInlineFullMainURL     = regexp.MustCompile(`https?://[^\s"'` + "`" + `<>)]+/main[.-][A-Za-z0-9_-]+\.js`)
	reInlineFullOndemandURL = regexp.MustCompile(`https?://[^\s"'` + "`" + `<>)]+/ondemand\.s[.-][A-Za-z0-9_-]+\.js`)
	reRelativeOndemandRef   = regexp.MustCompile(`["']((?:\./|\.\./|/responsive-web/)?[A-Za-z0-9_./-]*ondemand\.s[.-][A-Za-z0-9_-]+\.js)["']`)
	reInlineJSAssetURL      = regexp.MustCompile(`https?://[^\s"'` + "`" + `<>)]+/[A-Za-z0-9_.~-]+\.js`)
	reRelativeJSAssetRef    = regexp.MustCompile(`["']((?:\./|\.\./|/responsive-web/)[A-Za-z0-9_.~/-]+\.js)["']`)
)

// ParseHomeHTML extracts the twitter-site-verification key, 4 loading-x-anim SVG
// animation paths, main.*.js script URL, and (when present in HTML) ondemand.s.*.js URL.
// It supports both classic webpack and Vite x-web HTML shell formats.
func ParseHomeHTML(html string) (ParsedHomeHTML, error) {
	var out ParsedHomeHTML

	// 1. <meta name="twitter-site-verification" content="...">
	for _, tag := range reMetaTag.FindAllString(html, -1) {
		attrs := parseHTMLAttrs(tag)
		if strings.EqualFold(attrs["name"], "twitter-site-verification") && strings.TrimSpace(attrs["content"]) != "" {
			out.VerificationKey = strings.TrimSpace(attrs["content"])
			break
		}
	}
	if out.VerificationKey == "" {
		return ParsedHomeHTML{}, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindSchemaChanged,
			Message: "X 首页 HTML 缺少 twitter-site-verification 元标签",
		}
	}

	// 2. 4 SVG animation paths: loading-x-anim-0..3
	svgPaths := make([]string, txIDFrameCount)
	foundCount := 0
	for _, m := range reSVGBlock.FindAllStringSubmatch(html, -1) {
		attrs := parseHTMLAttrs(m[1])
		idMatch := reAnimID.FindStringSubmatch(strings.TrimSpace(attrs["id"]))
		if idMatch == nil {
			continue
		}
		idx, err := strconv.Atoi(idMatch[1])
		if err != nil || idx < 0 || idx >= txIDFrameCount {
			continue
		}
		pathD := selectAnimSVGPath(m[2])
		if pathD != "" && svgPaths[idx] == "" {
			svgPaths[idx] = pathD
			foundCount++
		}
	}
	if foundCount != txIDFrameCount {
		return ParsedHomeHTML{}, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindSchemaChanged,
			Message: "X 首页 HTML 缺少完整的 loading-x-anim SVG 动画路径",
		}
	}
	out.SVGPaths = svgPaths

	// 3. Script and preload/modulepreload URLs for main.*.js and ondemand.s.*.js.
	// Prefer modern non-legacy (non-nomodule, non--legacy/) bundles over client-web-legacy/.
	type assetCandidate struct {
		url    string
		legacy bool
	}
	var candidates []assetCandidate
	for _, tag := range reScriptTag.FindAllString(html, -1) {
		attrs := parseHTMLAttrs(tag)
		if src := strings.TrimSpace(attrs["src"]); src != "" {
			u := normalizeProtocolRelativeURL(src)
			candidates = append(candidates, assetCandidate{
				url:    u,
				legacy: isLegacyScriptAsset(tag, u),
			})
		}
	}
	for _, tag := range reLinkTag.FindAllString(html, -1) {
		attrs := parseHTMLAttrs(tag)
		if href := strings.TrimSpace(attrs["href"]); href != "" {
			u := normalizeProtocolRelativeURL(href)
			candidates = append(candidates, assetCandidate{
				url:    u,
				legacy: isLegacyScriptAsset(tag, u),
			})
		}
	}

	var fallbackMain, fallbackOndemand string
	for _, cand := range candidates {
		base := urlBaseName(cand.url)
		if reMainFile.MatchString(base) {
			if !cand.legacy && out.MainJSURL == "" {
				out.MainJSURL = cand.url
			} else if fallbackMain == "" {
				fallbackMain = cand.url
			}
		}
		if reOndemandFile.MatchString(base) {
			if !cand.legacy && out.OndemandJSURL == "" {
				out.OndemandJSURL = cand.url
			} else if fallbackOndemand == "" {
				fallbackOndemand = cand.url
			}
		}
	}
	if out.MainJSURL == "" {
		out.MainJSURL = fallbackMain
	}
	if out.OndemandJSURL == "" {
		out.OndemandJSURL = fallbackOndemand
	}
	if out.MainJSURL == "" {
		out.MainJSURL = pickPreferredAssetURL(reInlineFullMainURL.FindAllString(html, -1))
	}
	if out.MainJSURL == "" {
		return ParsedHomeHTML{}, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindSchemaChanged,
			Message: "X 首页 HTML 缺少 main.*.js 脚本地址",
		}
	}

	if out.OndemandJSURL == "" {
		out.OndemandJSURL = extractOndemandURLFromHTML(html, out.MainJSURL)
	}

	tagURLs := make([]string, 0, len(candidates))
	for _, cand := range candidates {
		if !cand.legacy && cand.url != "" {
			tagURLs = append(tagURLs, cand.url)
		}
	}
	out.LazyChunkURLs = extractLazyChunkURLsFromHTML(html, out.MainJSURL, tagURLs)

	return out, nil
}

func selectAnimSVGPath(svgInner string) string {
	pathTags := rePathTag.FindAllString(svgInner, -1)
	if len(pathTags) == 0 {
		return ""
	}
	paths := make([]string, 0, len(pathTags))
	for _, tag := range pathTags {
		attrs := parseHTMLAttrs(tag)
		if d := strings.TrimSpace(attrs["d"]); d != "" {
			paths = append(paths, d)
		}
	}
	if len(paths) >= 2 && strings.Contains(paths[1], "C") {
		return paths[1]
	}
	for _, d := range paths {
		if strings.Contains(d, "C") {
			return d
		}
	}
	return ""
}

func extractOndemandURLFromHTML(html, mainJSURL string) string {
	if m := pickPreferredAssetURL(reInlineFullOndemandURL.FindAllString(html, -1)); m != "" {
		return m
	}
	baseURL, baseErr := url.Parse(mainJSURL)

	// Vite relative or root-relative import/reference inside inline <script type="module">.
	if baseErr == nil {
		if m := reRelativeOndemandRef.FindStringSubmatch(html); len(m) == 2 {
			if ref, err := url.Parse(m[1]); err == nil {
				return baseURL.ResolveReference(ref).String()
			}
		}
	}

	// Classic webpack chunk hash map: direct "ondemand.s":"<hash>"
	if m := reWebpackOndemandDirect.FindStringSubmatch(html); len(m) == 2 {
		return resolveWebpackChunkURL(mainJSURL, "ondemand.s", m[1])
	}

	// Classic webpack numeric chunk ID indirection: 42:"ondemand.s" / "42":"ondemand.s" ... 42:"<hash>"
	if u := extractWebpackNumericOndemandURL(html, mainJSURL, true); u != "" {
		return u
	}

	return ""
}

func extractWebpackNumericOndemandURL(src, mainJSURL string, stopAtScriptClose bool) string {
	const maxPostMatchWindow = 256 * 1024
	locs := reWebpackOndemandNumID.FindAllStringSubmatchIndex(src, -1)
	if len(locs) == 0 {
		return ""
	}
	var fallbackURL string
	for _, loc := range locs {
		if len(loc) < 4 || loc[2] < 0 || loc[3] < 0 {
			continue
		}
		chunkID := src[loc[2]:loc[3]]
		tail := src[loc[1]:]
		if len(tail) > maxPostMatchWindow {
			tail = tail[:maxPostMatchWindow]
		}
		if stopAtScriptClose {
			if closeLoc := reScriptCloseTag.FindStringIndex(tail); closeLoc != nil {
				tail = tail[:closeLoc[0]]
			}
		}
		hashRe := regexp.MustCompile(`(?:\b|["'])` + regexp.QuoteMeta(chunkID) + `["']?\s*:\s*["']([A-Za-z0-9_-]{4,64})["']`)
		for _, hm := range hashRe.FindAllStringSubmatch(tail, -1) {
			if len(hm) != 2 || hm[1] == "ondemand.s" {
				continue
			}
			if reWebpackHexHash.MatchString(hm[1]) {
				return resolveWebpackChunkURL(mainJSURL, "ondemand.s", hm[1])
			}
			if fallbackURL == "" {
				fallbackURL = resolveWebpackChunkURL(mainJSURL, "ondemand.s", hm[1])
			}
		}
	}
	return fallbackURL
}

func isLegacyAssetURL(rawURL string) bool {
	return strings.Contains(strings.ToLower(rawURL), "-legacy/")
}

func isLegacyScriptAsset(tag, rawURL string) bool {
	return isLegacyAssetURL(rawURL) || reNoModuleAttr.MatchString(tag)
}

func pickPreferredAssetURL(urls []string) string {
	var fallback string
	for _, u := range urls {
		if u == "" {
			continue
		}
		if !isLegacyAssetURL(u) {
			return u
		}
		if fallback == "" {
			fallback = u
		}
	}
	return fallback
}

func extractOndemandURLFromMainJS(mainJS string, mainURL *url.URL) string {
	if m := pickPreferredAssetURL(reInlineFullOndemandURL.FindAllString(mainJS, -1)); m != "" {
		return m
	}
	if mainURL != nil {
		if m := reRelativeOndemandRef.FindStringSubmatch(mainJS); len(m) == 2 {
			if ref, err := url.Parse(m[1]); err == nil {
				return mainURL.ResolveReference(ref).String()
			}
		}
		if m := reWebpackOndemandDirect.FindStringSubmatch(mainJS); len(m) == 2 {
			return resolveWebpackChunkURL(mainURL.String(), "ondemand.s", m[1])
		}
		if u := extractWebpackNumericOndemandURL(mainJS, mainURL.String(), false); u != "" {
			return u
		}
	}
	return ""
}

type scoredLazyChunk struct {
	url       string
	chunkName string
	score     int
	order     int
}

func extractLazyChunkURLsFromHTML(html, mainJSURL string, tagURLs []string) map[string][]string {
	perOp := make(map[string][]scoredLazyChunk)
	order := 0
	addCand := func(rawURL, chunkName string) {
		if rawURL == "" || isLegacyAssetURL(rawURL) {
			return
		}
		for _, opName := range OptionalOperations() {
			sc := scoreLazyChunkName(opName, chunkName)
			if sc <= 0 {
				continue
			}
			order++
			perOp[opName] = append(perOp[opName], scoredLazyChunk{
				url:       rawURL,
				chunkName: chunkName,
				score:     sc,
				order:     order,
			})
		}
	}

	// 1. Preload/script tag URLs already collected from HTML.
	for _, u := range tagURLs {
		base := urlBaseName(u)
		if strings.HasSuffix(strings.ToLower(base), ".js") {
			addCand(u, base)
		}
	}

	// 2. Full inline JS URLs and relative JS references (e.g. Vite modulepreload/import).
	for _, u := range reInlineJSAssetURL.FindAllString(html, -1) {
		addCand(u, urlBaseName(u))
	}
	if baseURL, err := url.Parse(mainJSURL); err == nil {
		for _, m := range reRelativeJSAssetRef.FindAllStringSubmatch(html, -1) {
			if len(m) != 2 {
				continue
			}
			if ref, rErr := url.Parse(m[1]); rErr == nil {
				resolved := baseURL.ResolveReference(ref).String()
				addCand(resolved, urlBaseName(resolved))
			}
		}
	}

	// 3. Classic webpack chunk maps (both 2-table numeric chunk IDs and direct name->hash maps).
	extractWebpackLazyChunkCandidates(html, mainJSURL, true, addCand)

	return rankLazyChunkMap(perOp)
}

func extractLazyChunkURLsFromMainJS(mainJS string, mainURL *url.URL) map[string][]string {
	if mainJS == "" || mainURL == nil {
		return nil
	}
	mainJSURL := mainURL.String()
	perOp := make(map[string][]scoredLazyChunk)
	order := 0
	addCand := func(rawURL, chunkName string) {
		if rawURL == "" || isLegacyAssetURL(rawURL) {
			return
		}
		for _, opName := range OptionalOperations() {
			sc := scoreLazyChunkName(opName, chunkName)
			if sc <= 0 {
				continue
			}
			order++
			perOp[opName] = append(perOp[opName], scoredLazyChunk{
				url:       rawURL,
				chunkName: chunkName,
				score:     sc,
				order:     order,
			})
		}
	}

	for _, u := range reInlineJSAssetURL.FindAllString(mainJS, -1) {
		addCand(u, urlBaseName(u))
	}
	for _, m := range reRelativeJSAssetRef.FindAllStringSubmatch(mainJS, -1) {
		if len(m) != 2 {
			continue
		}
		if ref, rErr := url.Parse(m[1]); rErr == nil {
			resolved := mainURL.ResolveReference(ref).String()
			addCand(resolved, urlBaseName(resolved))
		}
	}
	extractWebpackLazyChunkCandidates(mainJS, mainJSURL, false, addCand)

	return rankLazyChunkMap(perOp)
}

func extractWebpackLazyChunkCandidates(
	src, mainJSURL string,
	stopAtScriptClose bool,
	add func(rawURL, chunkName string),
) {
	const maxPostMatchWindow = 256 * 1024

	// A. Direct name->hash map: "bundle.Bookmarks":"<hash>", "shared~bundle.Bookmarks":"<hash>", "endpoints":"<hash>"
	for _, m := range reWebpackChunkDirect.FindAllStringSubmatch(src, -1) {
		if len(m) != 3 {
			continue
		}
		chunkName, hash := m[1], m[2]
		if !isSafeWebpackChunkName(chunkName) || !isExplicitWebpackChunkName(chunkName) {
			continue
		}
		if strings.EqualFold(chunkName, "api") && !reWebpackHexHash.MatchString(hash) {
			continue
		}
		if !matchesAnyOptionalChunk(chunkName) {
			continue
		}
		add(resolveWebpackChunkURL(mainJSURL, chunkName, hash), chunkName)
	}

	// B. Two-table numeric chunk ID map: 412:"bundle.Bookmarks" ... 412:"<hash>"
	for _, loc := range reWebpackChunkNumID.FindAllStringSubmatchIndex(src, -1) {
		if len(loc) < 6 || loc[2] < 0 || loc[3] < 0 || loc[4] < 0 || loc[5] < 0 {
			continue
		}
		chunkID := src[loc[2]:loc[3]]
		chunkName := src[loc[4]:loc[5]]
		if !isSafeWebpackChunkName(chunkName) || !matchesAnyOptionalChunk(chunkName) {
			continue
		}
		tail := src[loc[1]:]
		if len(tail) > maxPostMatchWindow {
			tail = tail[:maxPostMatchWindow]
		}
		if stopAtScriptClose {
			if closeLoc := reScriptCloseTag.FindStringIndex(tail); closeLoc != nil {
				tail = tail[:closeLoc[0]]
			}
		}
		hashRe := regexp.MustCompile(`(?:\b|["'])` + regexp.QuoteMeta(chunkID) + `["']?\s*:\s*["']([A-Za-z0-9_-]{4,32})["']`)
		var bestHash, fallbackHash string
		for _, hm := range hashRe.FindAllStringSubmatch(tail, -1) {
			if len(hm) != 2 || hm[1] == chunkName {
				continue
			}
			if reWebpackHexHash.MatchString(hm[1]) {
				bestHash = hm[1]
				break
			}
			if fallbackHash == "" {
				fallbackHash = hm[1]
			}
		}
		chosen := bestHash
		if chosen == "" {
			chosen = fallbackHash
		}
		if chosen != "" {
			add(resolveWebpackChunkURL(mainJSURL, chunkName, chosen), chunkName)
		}
	}
}

func isSafeWebpackChunkName(name string) bool {
	if name == "" || strings.Contains(name, "..") || strings.ContainsAny(name, "/\\?# \t\r\n") {
		return false
	}
	return true
}

func isExplicitWebpackChunkName(name string) bool {
	lower := strings.ToLower(strings.TrimSpace(name))
	return strings.HasPrefix(lower, "bundle.") ||
		strings.HasPrefix(lower, "shared~") ||
		strings.HasPrefix(lower, "loader.") ||
		strings.HasPrefix(lower, "ondemand.") ||
		strings.HasPrefix(lower, "chunk.") ||
		strings.HasPrefix(lower, "graphql.") ||
		lower == "endpoints" ||
		strings.HasPrefix(lower, "endpoints.") ||
		lower == "api"
}

func matchesAnyOptionalChunk(chunkName string) bool {
	for _, opName := range OptionalOperations() {
		if scoreLazyChunkName(opName, chunkName) > 0 {
			return true
		}
	}
	return false
}

func scoreLazyChunkName(opName, chunkName string) int {
	name := strings.ToLower(strings.TrimSpace(chunkName))
	if name == "" || strings.Contains(name, "..") || strings.ContainsAny(name, " \t\r\n?#\\") {
		return 0
	}
	name = strings.TrimSuffix(name, ".js")
	if name == "" ||
		reMainFile.MatchString(name+".js") ||
		reOndemandFile.MatchString(name+".js") ||
		name == "ondemand.s" ||
		strings.HasPrefix(name, "main.") ||
		strings.HasPrefix(name, "main-") ||
		strings.HasPrefix(name, "i18n") ||
		strings.Contains(name, "i18n/") ||
		strings.Contains(name, "emoji") ||
		strings.Contains(name, "icons") ||
		strings.HasPrefix(name, "vendor") ||
		strings.HasPrefix(name, "runtime") ||
		strings.HasPrefix(name, "polyfills") {
		return 0
	}

	switch opName {
	case OpBookmarks:
		switch {
		case name == "bundle.bookmarks" || strings.HasPrefix(name, "bundle.bookmarks.") || strings.HasPrefix(name, "bundle.bookmarks-"):
			return 110
		case strings.Contains(name, "bundle.bookmarks"):
			return 100
		case name == "bookmarks" || strings.HasPrefix(name, "bookmarks.") || strings.HasPrefix(name, "bookmarks-"):
			return 95
		case strings.Contains(name, "bookmark"):
			return 80
		case name == "endpoints" || strings.Contains(name, "endpoints") || strings.Contains(name, "graphql"):
			return 50
		case name == "api" || strings.HasPrefix(name, "api.") || strings.HasPrefix(name, "api-"):
			return 45
		}
	case OpLikes:
		switch {
		case name == "bundle.likes" || strings.HasPrefix(name, "bundle.likes.") || strings.HasPrefix(name, "bundle.likes-"):
			return 110
		case strings.Contains(name, "bundle.likes"):
			return 100
		case name == "likes" || strings.HasPrefix(name, "likes.") || strings.HasPrefix(name, "likes-"):
			return 95
		case containsChunkToken(name, "likes"):
			return 80
		case name == "bundle.profile" || strings.HasPrefix(name, "bundle.profile.") || strings.HasPrefix(name, "bundle.profile-"):
			return 65
		case strings.Contains(name, "bundle.profile"):
			return 60
		case name == "endpoints" || strings.Contains(name, "endpoints") || strings.Contains(name, "graphql"):
			return 50
		case name == "api" || strings.HasPrefix(name, "api.") || strings.HasPrefix(name, "api-"):
			return 45
		}
	}
	return 0
}

func containsChunkToken(name, token string) bool {
	parts := strings.FieldsFunc(name, func(r rune) bool {
		return r == '.' || r == '~' || r == '-' || r == '_' || r == '/'
	})
	for _, p := range parts {
		if p == token {
			return true
		}
	}
	return false
}

func rankLazyChunkMap(perOp map[string][]scoredLazyChunk) map[string][]string {
	if len(perOp) == 0 {
		return nil
	}
	out := make(map[string][]string, len(perOp))
	for opName, list := range perOp {
		if len(list) == 0 {
			continue
		}
		sort.SliceStable(list, func(i, j int) bool {
			if list[i].score != list[j].score {
				return list[i].score > list[j].score
			}
			if len(list[i].chunkName) != len(list[j].chunkName) {
				return len(list[i].chunkName) < len(list[j].chunkName)
			}
			return list[i].order < list[j].order
		})
		seen := make(map[string]bool, len(list))
		urls := make([]string, 0, min(len(list), maxLazyCandidatesPerOp))
		for _, item := range list {
			if seen[item.url] {
				continue
			}
			seen[item.url] = true
			urls = append(urls, item.url)
			if len(urls) >= maxLazyCandidatesPerOp {
				break
			}
		}
		if len(urls) > 0 {
			out[opName] = urls
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func filterAndMergeLazyChunkURLs(allowLocalhost bool, maps ...map[string][]string) map[string][]string {
	out := make(map[string][]string)
	for _, opName := range OptionalOperations() {
		seen := make(map[string]bool)
		var urls []string
		for _, m := range maps {
			for _, rawURL := range m[opName] {
				u, err := ValidateStaticAssetURL(rawURL, allowLocalhost)
				if err != nil {
					continue
				}
				clean := u.String()
				if seen[clean] {
					continue
				}
				seen[clean] = true
				urls = append(urls, clean)
				if len(urls) >= maxLazyCandidatesPerOp {
					break
				}
			}
			if len(urls) >= maxLazyCandidatesPerOp {
				break
			}
		}
		if len(urls) > 0 {
			out[opName] = urls
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func resolveWebpackChunkURL(mainJSURL, chunkName, hash string) string {
	u, err := url.Parse(mainJSURL)
	if err != nil {
		return "https://" + StaticAssetHost + StaticAssetPathPrefix + "client-web/" + chunkName + "." + hash + "a.js"
	}
	dir := path.Dir(u.Path)
	if dir == "." || dir == "/" {
		dir = StaticAssetPathPrefix + "client-web"
	}
	file := chunkName + "." + hash + "a.js"
	u.Path = strings.TrimSuffix(dir, "/") + "/" + file
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

func normalizeProtocolRelativeURL(raw string) string {
	if strings.HasPrefix(raw, "//") {
		return "https:" + raw
	}
	return raw
}

func urlBaseName(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return path.Base(u.Path)
}

func parseHTMLAttrs(tag string) map[string]string {
	attrs := make(map[string]string)
	for _, m := range reHTMLAttr.FindAllStringSubmatch(tag, -1) {
		key := strings.ToLower(strings.TrimSpace(m[1]))
		val := m[2]
		if val == "" && m[3] != "" {
			val = m[3]
		} else if val == "" && m[4] != "" {
			val = m[4]
		}
		attrs[key] = val
	}
	return attrs
}

// ---------- main.*.js Parsing (Webpack + Vite) ----------

var (
	reExplicitBearerLit = regexp.MustCompile(`["'` + "`" + `]Bearer\s+([A-Za-z0-9%+/=._-]{16,256})["'` + "`" + `]`)
	reNamedBearerAssign = regexp.MustCompile(`(?i)\b(?:bearer[_]?token|webBearer|public[_]?bearer)\b\s*[:=]\s*["'` + "`" + `]([A-Za-z0-9%+/=._-]{16,256})["'` + "`" + `]`)
	reClassicWebBearer  = regexp.MustCompile(`["'` + "`" + `](A{16,}[A-Za-z0-9%+/=_-]{40,200})["'` + "`" + `]`)
	reValidQueryID      = regexp.MustCompile(`^[A-Za-z0-9_-]{6,64}$`)
	reOpNameMatch       = regexp.MustCompile(`(?:["']?operationName["']?|["']?name["']?)\s*:\s*["'](UserByScreenName|UserByRestId|UserTweets|TweetDetail|Following|Bookmarks|Likes)["']`)
	reValueBoolDefault  = regexp.MustCompile(`(?:["']([A-Za-z0-9_.-]+)["']|\b([A-Za-z_][A-Za-z0-9_]*))\s*:\s*\{\s*["']?value["']?\s*:\s*(true|false|!0|!1)\b`)
	reFlatBoolDefault   = regexp.MustCompile(`(?:["']([A-Za-z0-9_.-]+)["']|\b([A-Za-z_][A-Za-z0-9_]*))\s*:\s*(true|false|!0|!1)\b`)
)

type rawOpCandidate struct {
	opName            string
	queryID           string
	featuresMap       map[string]bool
	featureSwitchKeys []string
	fieldTogglesMap   map[string]bool
	fieldToggleKeys   []string
}

// ParseMainBundle extracts the public web Bearer token, the 5 core GraphQL operations
// (queryId, features, fieldToggles), and any optional Mine operations (Bookmarks, Likes)
// present in a main.*.js bundle in either classic webpack or Vite x-web module syntax.
// Missing Bookmarks or Likes in main.*.js does NOT fail core discovery; they are
// resolved lazily on demand via FrontendDiscovery.EnsureOperation.
func ParseMainBundle(js string) (string, map[string]GraphQLOperationSpec, error) {
	return parseMainBundle(js, "")
}

func parseMainBundle(js, homeHTML string) (string, map[string]GraphQLOperationSpec, error) {
	bearer := extractBearerToken(js)
	if bearer == "" {
		return "", nil, &site.Error{
			Site:    SiteID,
			Kind:    site.ErrKindSchemaChanged,
			Message: "X 前端主脚本缺少公开网页 Bearer 令牌",
		}
	}

	candidates := extractOperationCandidates(js)
	neededKeys := collectCandidateSwitchKeys(candidates)
	defaults := scanJSBoolDefaults(neededKeys, js, homeHTML)
	ops := materializeOperationSpecs(candidates, defaults)

	// Require ONLY the 5 core operations in main.*.js; Bookmarks/Likes are optional
	// here and resolved lazily on demand when code-split into separate chunks.
	for _, opName := range CoreOperations() {
		spec, ok := ops[opName]
		if !ok || !reValidQueryID.MatchString(spec.QueryID) {
			return "", nil, &site.Error{
				Site:    SiteID,
				Kind:    site.ErrKindSchemaChanged,
				Message: fmt.Sprintf("X 前端主脚本缺少必需操作 %s 的 queryId", opName),
			}
		}
	}

	return bearer, ops, nil
}

func parseLazyChunkOperations(chunkJS string, baseDefaults map[string]bool) map[string]GraphQLOperationSpec {
	candidates := extractOperationCandidates(chunkJS)
	if len(candidates) == 0 {
		return nil
	}
	neededKeys := collectCandidateSwitchKeys(candidates)
	defaults := scanJSBoolDefaults(neededKeys, chunkJS)
	for k := range neededKeys {
		if baseVal, ok := baseDefaults[k]; ok {
			defaults[k] = baseVal
		}
	}
	return materializeOperationSpecs(candidates, defaults)
}

func extractOperationCandidates(js string) map[string]rawOpCandidate {
	candidates := make(map[string]rawOpCandidate, 7)
	for _, loc := range reOpNameMatch.FindAllStringSubmatchIndex(js, -1) {
		opName := js[loc[2]:loc[3]]
		objSrc, ok := extractEnclosingJSObject(js, loc[0])
		if !ok {
			continue
		}
		parsedObj, ok := parseJSObject(objSrc)
		if !ok {
			continue
		}

		cand, valid := decodeOperationCandidate(opName, parsedObj)
		if !valid {
			// Check one outer level for wrapped shapes like { params: { ... } }
			outerStart := findEnclosingBraceStart(js, loc[0])
			if outerStart > 0 {
				if outerSrc, ok2 := extractEnclosingJSObject(js, outerStart-1); ok2 {
					if outerObj, ok3 := parseJSObject(outerSrc); ok3 {
						cand, valid = decodeOperationCandidate(opName, outerObj)
					}
				}
			}
		}
		if !valid {
			continue
		}

		prev, exists := candidates[opName]
		if !exists || candidateScore(cand.featuresMap, cand.featureSwitchKeys) > candidateScore(prev.featuresMap, prev.featureSwitchKeys) {
			candidates[opName] = cand
		}
	}
	return candidates
}

func collectCandidateSwitchKeys(candidates map[string]rawOpCandidate) map[string]struct{} {
	neededKeys := make(map[string]struct{})
	for _, cand := range candidates {
		for _, k := range cand.featureSwitchKeys {
			neededKeys[k] = struct{}{}
		}
		for _, k := range cand.fieldToggleKeys {
			neededKeys[k] = struct{}{}
		}
	}
	return neededKeys
}

func materializeOperationSpecs(
	candidates map[string]rawOpCandidate,
	defaults map[string]bool,
) map[string]GraphQLOperationSpec {
	ops := make(map[string]GraphQLOperationSpec, len(candidates))
	for opName, cand := range candidates {
		if !reValidQueryID.MatchString(cand.queryID) {
			continue
		}
		features := make(map[string]bool)
		for k, v := range cand.featuresMap {
			features[k] = v
		}
		for _, k := range cand.featureSwitchKeys {
			if _, already := features[k]; !already {
				features[k] = defaults[k]
			}
		}

		var fieldToggles map[string]bool
		if len(cand.fieldTogglesMap) > 0 || len(cand.fieldToggleKeys) > 0 {
			fieldToggles = make(map[string]bool)
			for k, v := range cand.fieldTogglesMap {
				fieldToggles[k] = v
			}
			for _, k := range cand.fieldToggleKeys {
				if _, already := fieldToggles[k]; !already {
					fieldToggles[k] = defaults[k]
				}
			}
		}

		ops[opName] = GraphQLOperationSpec{
			OperationName: opName,
			QueryID:       cand.queryID,
			Features:      features,
			FieldToggles:  fieldToggles,
		}
	}
	return ops
}

func extractBearerToken(js string) string {
	for _, re := range []*regexp.Regexp{reClassicWebBearer, reExplicitBearerLit, reNamedBearerAssign} {
		for _, m := range re.FindAllStringSubmatch(js, -1) {
			if len(m) == 2 {
				tok := strings.TrimSpace(m[1])
				if isValidBearerToken(tok) {
					return tok
				}
			}
		}
	}
	return ""
}

func isValidBearerToken(tok string) bool {
	if len(tok) < 16 || len(tok) > 512 {
		return false
	}
	if strings.Contains(tok, "${") || strings.ContainsAny(tok, " \t\r\n") {
		return false
	}
	return true
}

func candidateScore(m map[string]bool, keys []string) int {
	return len(m) + len(keys)
}

func decodeOperationCandidate(expectedOp string, obj map[string]any) (rawOpCandidate, bool) {
	var out rawOpCandidate

	// Unwrap Vite/Relay { params: { id, name, metadata } } wrapper if present.
	if params, ok := obj["params"].(map[string]any); ok {
		obj = params
	}

	opName := firstStringField(obj, "operationName", "name")
	if opName != expectedOp {
		return out, false
	}
	opType := strings.ToLower(firstStringField(obj, "operationType", "operationKind"))
	if opType != "" && opType != "query" {
		return out, false
	}

	qid := firstStringField(obj, "queryId", "id")
	if !reValidQueryID.MatchString(qid) {
		return out, false
	}

	out.opName = opName
	out.queryID = qid

	extractSwitches := func(src map[string]any) {
		if src == nil {
			return
		}
		if fMap, ok := toBoolMap(src["features"]); ok && len(fMap) > 0 {
			out.featuresMap = mergeBoolMaps(out.featuresMap, fMap)
		}
		if fsMap, ok := toBoolMap(src["featureSwitches"]); ok && len(fsMap) > 0 {
			out.featuresMap = mergeBoolMaps(out.featuresMap, fsMap)
		} else if fsList, ok := toStringSlice(src["featureSwitches"]); ok && len(fsList) > 0 {
			out.featureSwitchKeys = append(out.featureSwitchKeys, fsList...)
		}
		if ftMap, ok := toBoolMap(src["fieldToggles"]); ok && len(ftMap) > 0 {
			out.fieldTogglesMap = mergeBoolMaps(out.fieldTogglesMap, ftMap)
		} else if ftList, ok := toStringSlice(src["fieldToggles"]); ok && len(ftList) > 0 {
			out.fieldToggleKeys = append(out.fieldToggleKeys, ftList...)
		}
	}

	extractSwitches(obj)
	if meta, ok := obj["metadata"].(map[string]any); ok {
		extractSwitches(meta)
	}
	return out, true
}

func firstStringField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func toBoolMap(v any) (map[string]bool, bool) {
	m, ok := v.(map[string]any)
	if !ok {
		return nil, false
	}
	out := make(map[string]bool, len(m))
	for k, raw := range m {
		if b, ok := raw.(bool); ok {
			out[k] = b
		}
	}
	return out, len(out) > 0
}

func toStringSlice(v any) ([]string, bool) {
	arr, ok := v.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(arr))
	for _, raw := range arr {
		if s, ok := raw.(string); ok && s != "" {
			out = append(out, s)
		}
	}
	return out, len(out) > 0
}

func mergeBoolMaps(dst, src map[string]bool) map[string]bool {
	if dst == nil {
		dst = make(map[string]bool, len(src))
	}
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func scanJSBoolDefaults(needed map[string]struct{}, sources ...string) map[string]bool {
	out := make(map[string]bool, len(needed))
	if len(needed) == 0 {
		return out
	}
	// Pass 1: flat defaults key:true / key:!0 / key:false / key:!1 across all sources.
	for _, src := range sources {
		if src == "" {
			continue
		}
		for _, m := range reFlatBoolDefault.FindAllStringSubmatch(src, -1) {
			k := m[1]
			if k == "" {
				k = m[2]
			}
			if _, want := needed[k]; want {
				out[k] = parseJSBoolToken(m[3])
			}
		}
	}
	// Pass 2: config objects key:{value:!0} (webpack defaultConfig & __INITIAL_STATE__
	// featureSwitch.defaultConfig / featureSwitch.user.config) take precedence.
	// Later sources (homeBody __INITIAL_STATE__) override earlier static bundle defaults.
	for _, src := range sources {
		if src == "" {
			continue
		}
		for _, m := range reValueBoolDefault.FindAllStringSubmatch(src, -1) {
			k := m[1]
			if k == "" {
				k = m[2]
			}
			if _, want := needed[k]; want {
				out[k] = parseJSBoolToken(m[3])
			}
		}
	}
	return out
}

// scanAllFeatureDefaults extracts featureSwitch/fieldToggle boolean defaults from main.*.js
// and home HTML (__INITIAL_STATE__) so lazy chunks fetched later can resolve their
// featureSwitches lists accurately without retaining raw HTML/JS bundles in memory.
func scanAllFeatureDefaults(sources ...string) map[string]bool {
	out := make(map[string]bool)
	for _, src := range sources {
		if src == "" {
			continue
		}
		for _, m := range reFlatBoolDefault.FindAllStringSubmatch(src, -1) {
			k := m[1]
			if k == "" {
				k = m[2]
			}
			if len(k) >= 4 {
				out[k] = parseJSBoolToken(m[3])
			}
		}
	}
	for _, src := range sources {
		if src == "" {
			continue
		}
		for _, m := range reValueBoolDefault.FindAllStringSubmatch(src, -1) {
			k := m[1]
			if k == "" {
				k = m[2]
			}
			if k != "" {
				out[k] = parseJSBoolToken(m[3])
			}
		}
	}
	return out
}

func parseJSBoolToken(tok string) bool {
	return tok == "true" || tok == "!0"
}

// ---------- Bounded JS Object Extractor & Mini Parser ----------

func findEnclosingBraceStart(src string, pos int) int {
	const maxBackScan = 8192
	limit := pos - maxBackScan
	if limit < 0 {
		limit = 0
	}
	depth := 0
	inStr := byte(0)
	for i := pos - 1; i >= limit; i-- {
		c := src[i]
		if inStr != 0 {
			if c == inStr && !isEscapedAt(src, i) {
				inStr = 0
			}
			continue
		}
		switch c {
		case '"', '\'', '`':
			inStr = c
		case '}':
			depth++
		case '{':
			if depth == 0 {
				return i
			}
			depth--
		}
	}
	return -1
}

func extractEnclosingJSObject(src string, pos int) (string, bool) {
	start := findEnclosingBraceStart(src, pos)
	if start < 0 {
		return "", false
	}
	const maxObjLen = 32768
	endLimit := start + maxObjLen
	if endLimit > len(src) {
		endLimit = len(src)
	}
	depth := 0
	inStr := byte(0)
	for i := start; i < endLimit; i++ {
		c := src[i]
		if inStr != 0 {
			if c == '\\' {
				i++
				continue
			}
			if c == inStr {
				inStr = 0
			}
			continue
		}
		switch c {
		case '"', '\'', '`':
			inStr = c
		case '/':
			if i+1 < endLimit && src[i+1] == '/' {
				for i < endLimit && src[i] != '\n' {
					i++
				}
			} else if i+1 < endLimit && src[i+1] == '*' {
				i += 2
				for i+1 < endLimit && !(src[i] == '*' && src[i+1] == '/') {
					i++
				}
				i++
			}
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return src[start : i+1], true
			}
		}
	}
	return "", false
}

func isEscapedAt(s string, idx int) bool {
	bs := 0
	for i := idx - 1; i >= 0 && s[i] == '\\'; i-- {
		bs++
	}
	return bs%2 == 1
}

type jsParser struct {
	s string
	i int
}

func parseJSObject(src string) (map[string]any, bool) {
	p := &jsParser{s: src}
	p.skipWS()
	v, ok := p.parseObject()
	return v, ok
}

func (p *jsParser) skipWS() {
	for p.i < len(p.s) {
		c := p.s[p.i]
		if c == ' ' || c == '\t' || c == '\r' || c == '\n' {
			p.i++
			continue
		}
		if c == '/' && p.i+1 < len(p.s) && p.s[p.i+1] == '/' {
			p.i += 2
			for p.i < len(p.s) && p.s[p.i] != '\n' {
				p.i++
			}
			continue
		}
		if c == '/' && p.i+1 < len(p.s) && p.s[p.i+1] == '*' {
			p.i += 2
			for p.i+1 < len(p.s) && !(p.s[p.i] == '*' && p.s[p.i+1] == '/') {
				p.i++
			}
			if p.i+1 < len(p.s) {
				p.i += 2
			}
			continue
		}
		break
	}
}

func (p *jsParser) parseObject() (map[string]any, bool) {
	p.skipWS()
	if p.i >= len(p.s) || p.s[p.i] != '{' {
		return nil, false
	}
	p.i++
	obj := make(map[string]any)
	for {
		p.skipWS()
		if p.i >= len(p.s) {
			return nil, false
		}
		if p.s[p.i] == '}' {
			p.i++
			return obj, true
		}
		key, ok := p.parseKey()
		if !ok {
			return nil, false
		}
		p.skipWS()
		if p.i >= len(p.s) || p.s[p.i] != ':' {
			return nil, false
		}
		p.i++
		val := p.parseValue()
		obj[key] = val
		p.skipWS()
		if p.i < len(p.s) && p.s[p.i] == ',' {
			p.i++
			continue
		}
		if p.i < len(p.s) && p.s[p.i] == '}' {
			p.i++
			return obj, true
		}
		return nil, false
	}
}

func (p *jsParser) parseArray() ([]any, bool) {
	p.skipWS()
	if p.i >= len(p.s) || p.s[p.i] != '[' {
		return nil, false
	}
	p.i++
	var arr []any
	for {
		p.skipWS()
		if p.i >= len(p.s) {
			return nil, false
		}
		if p.s[p.i] == ']' {
			p.i++
			return arr, true
		}
		arr = append(arr, p.parseValue())
		p.skipWS()
		if p.i < len(p.s) && p.s[p.i] == ',' {
			p.i++
			continue
		}
		if p.i < len(p.s) && p.s[p.i] == ']' {
			p.i++
			return arr, true
		}
		return nil, false
	}
}

func (p *jsParser) parseKey() (string, bool) {
	p.skipWS()
	if p.i >= len(p.s) {
		return "", false
	}
	if p.s[p.i] == '"' || p.s[p.i] == '\'' || p.s[p.i] == '`' {
		return p.parseString()
	}
	start := p.i
	for p.i < len(p.s) {
		c := p.s[p.i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '$' {
			p.i++
			continue
		}
		break
	}
	if p.i == start {
		return "", false
	}
	return p.s[start:p.i], true
}

func (p *jsParser) parseString() (string, bool) {
	if p.i >= len(p.s) {
		return "", false
	}
	quote := p.s[p.i]
	if quote != '"' && quote != '\'' && quote != '`' {
		return "", false
	}
	p.i++
	var b strings.Builder
	for p.i < len(p.s) {
		c := p.s[p.i]
		p.i++
		if c == quote {
			return b.String(), true
		}
		if c == '\\' && p.i < len(p.s) {
			esc := p.s[p.i]
			p.i++
			switch esc {
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case '\\', '"', '\'', '`', '/':
				b.WriteByte(esc)
			case 'u':
				if p.i+4 <= len(p.s) {
					if r, err := strconv.ParseInt(p.s[p.i:p.i+4], 16, 32); err == nil {
						b.WriteRune(rune(r))
						p.i += 4
						continue
					}
				}
				b.WriteByte('u')
			default:
				b.WriteByte(esc)
			}
			continue
		}
		b.WriteByte(c)
	}
	return "", false
}

func (p *jsParser) parseValue() any {
	p.skipWS()
	if p.i >= len(p.s) {
		return nil
	}
	switch p.s[p.i] {
	case '{':
		if obj, ok := p.parseObject(); ok {
			return obj
		}
		p.skipUnknownExpr()
		return nil
	case '[':
		if arr, ok := p.parseArray(); ok {
			return arr
		}
		p.skipUnknownExpr()
		return nil
	case '"', '\'', '`':
		if s, ok := p.parseString(); ok {
			return s
		}
		return nil
	}

	if strings.HasPrefix(p.s[p.i:], "true") && p.isTokenEnd(p.i+4) {
		p.i += 4
		return true
	}
	if strings.HasPrefix(p.s[p.i:], "false") && p.isTokenEnd(p.i+5) {
		p.i += 5
		return false
	}
	if strings.HasPrefix(p.s[p.i:], "!0") && p.isTokenEnd(p.i+2) {
		p.i += 2
		return true
	}
	if strings.HasPrefix(p.s[p.i:], "!1") && p.isTokenEnd(p.i+2) {
		p.i += 2
		return false
	}
	p.skipUnknownExpr()
	return nil
}

func (p *jsParser) isTokenEnd(idx int) bool {
	if idx >= len(p.s) {
		return true
	}
	c := p.s[idx]
	return !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '$')
}

func (p *jsParser) skipUnknownExpr() {
	depth := 0
	for p.i < len(p.s) {
		c := p.s[p.i]
		if c == '"' || c == '\'' || c == '`' {
			_, _ = p.parseString()
			continue
		}
		switch c {
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth == 0 {
				return
			}
			depth--
		case ',':
			if depth == 0 {
				return
			}
		}
		p.i++
	}
}
