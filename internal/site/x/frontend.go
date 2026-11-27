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

	maxStaticAssetRedirects    = 3
	maxLazyChunksPerOperation  = 2
	maxSpecificLazyChunksPerOp = 4
	highConfidenceLazyScore    = 70
	maxLazyCandidatesPerOp     = 12
	maxRetainedManifestChunks  = 4096
)

// Read-only GraphQL operation names used by TRACE for X (x.md §15.1 #8, DEC-092, DEC-093).
const (
	OpUserByScreenName         = "UserByScreenName"
	OpUserByRestId             = "UserByRestId"
	OpUserTweets               = "UserTweets"
	OpUserTweetsAndReplies     = "UserTweetsAndReplies"
	OpUserMedia                = "UserMedia"
	OpUserHighlightsTweets     = "UserHighlightsTweets"
	OpUserArticlesTweets       = "UserArticlesTweets"
	OpTweetDetail              = "TweetDetail"
	OpFollowing                = "Following"
	OpBookmarks                = "Bookmarks"
	OpLikes                    = "Likes"
	OpCombinedLists            = "CombinedLists"
	OpListOwnerships           = "ListOwnerships"
	OpListByRestId             = "ListByRestId"
	OpListLatestTweetsTimeline = "ListLatestTweetsTimeline"
	OpListMembers              = "ListMembers"
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

// ProfileTabOperations returns a fresh copy of the 4 optional X user profile tab
// GraphQL operations discovered from main.*.js (DEC-092, DEC-093). Their absence
// MUST NOT block login or core UserTweets.
func ProfileTabOperations() []string {
	return []string{
		OpUserTweetsAndReplies,
		OpUserMedia,
		OpUserHighlightsTweets,
		OpUserArticlesTweets,
	}
}

// ListsOperations returns a fresh copy of the 5 read-only X Lists GraphQL operation
// names discovered via 2-hop lazy chunks (DEC-093). Their absence MUST NOT block
// core login or timeline features.
func ListsOperations() []string {
	return []string{
		OpCombinedLists,
		OpListOwnerships,
		OpListByRestId,
		OpListLatestTweetsTimeline,
		OpListMembers,
	}
}

// IsListsOperation reports whether opName is one of the read-only X Lists GraphQL operations.
func IsListsOperation(opName string) bool {
	switch opName {
	case OpCombinedLists, OpListOwnerships, OpListByRestId, OpListLatestTweetsTimeline, OpListMembers:
		return true
	default:
		return false
	}
}

// OptionalOperations returns a fresh copy of the read-only optional GraphQL operation
// names ("Mine" operations, profile tab operations, and public X Lists operations).
// Their absence from main.*.js MUST NOT fail core frontend discovery or login; they
// are resolved on demand via EnsureOperation.
func OptionalOperations() []string {
	return []string{
		OpBookmarks,
		OpLikes,
		OpUserTweetsAndReplies,
		OpUserMedia,
		OpUserHighlightsTweets,
		OpUserArticlesTweets,
		OpCombinedLists,
		OpListOwnerships,
		OpListByRestId,
		OpListLatestTweetsTimeline,
		OpListMembers,
	}
}

// IsOptionalOperation reports whether opName is an optional GraphQL operation whose
// absence must not block core login/UserTweets or trip the site-wide circuit breaker.
func IsOptionalOperation(opName string) bool {
	switch opName {
	case OpBookmarks,
		OpLikes,
		OpUserTweetsAndReplies,
		OpUserMedia,
		OpUserHighlightsTweets,
		OpUserArticlesTweets,
		OpCombinedLists,
		OpListOwnerships,
		OpListByRestId,
		OpListLatestTweetsTimeline,
		OpListMembers:
		return true
	default:
		return false
	}
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

	featureDefaults map[string]bool           `json:"-"`
	lazyChunkURLs   map[string][]string       `json:"-"`
	lazyChunkScores map[string]map[string]int `json:"-"`
	chunkURLByID    map[string]string         `json:"-"`
	fetchedLazyURLs map[string]bool           `json:"-"`
}

// FeatureDefault looks up a boolean featureSwitch / fieldToggle default value discovered
// from main.*.js or home HTML (__INITIAL_STATE__).
func (m *FrontendMetadata) FeatureDefault(key string) (bool, bool) {
	if m == nil || len(m.featureDefaults) == 0 {
		return false, false
	}
	v, ok := m.featureDefaults[key]
	return v, ok
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
		lazyChunkScores: m.lazyChunkScores,
		chunkURLByID:    m.chunkURLByID,
		fetchedLazyURLs: fetched,
	}
}

func (m *FrontendMetadata) cloneWithExtraOpFeatures(opName string, extra map[string]bool) *FrontendMetadata {
	if m == nil || len(extra) == 0 {
		return m
	}
	spec, ok := m.Operations[opName]
	if !ok || spec.QueryID == "" {
		return m
	}
	mergedFeatures := make(map[string]bool, len(spec.Features)+len(extra))
	for k, v := range spec.Features {
		mergedFeatures[k] = v
	}
	changed := false
	for k, v := range extra {
		if cur, exists := mergedFeatures[k]; !exists || cur != v {
			mergedFeatures[k] = v
			changed = true
		}
	}
	if !changed {
		return m
	}
	updatedSpec := GraphQLOperationSpec{
		OperationName: spec.OperationName,
		QueryID:       spec.QueryID,
		Features:      mergedFeatures,
		FieldToggles:  spec.FieldToggles,
	}
	return m.cloneWithLazyUpdates(map[string]GraphQLOperationSpec{opName: updatedSpec}, nil)
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

	lazyChunkScores map[string]map[string]int
	chunkURLByID    map[string]string
	opChunkIDs      map[string]map[string]bool
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

// MergeOperationFeatures publishes an updated immutable FrontendMetadata snapshot in memory
// with extra non-null boolean feature switches merged into opName's GraphQLOperationSpec.
func (d *FrontendDiscovery) MergeOperationFeatures(opName string, extra map[string]bool) *FrontendMetadata {
	if d == nil || len(extra) == 0 {
		return nil
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.current == nil {
		return nil
	}
	updated := d.current.cloneWithExtraOpFeatures(opName, extra)
	d.current = updated
	return updated
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
	flightTimeout := d.assetTimeout*maxSpecificLazyChunksPerOp + 10*time.Second
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

	type lazyQueueItem struct {
		url   string
		score int
	}
	scoreMap := baseMeta.lazyChunkScores[opName]
	queue := make([]lazyQueueItem, 0, len(candidates)+4)
	queuedSet := make(map[string]bool, len(candidates)+4)
	fetchedSet := make(map[string]bool, len(baseMeta.fetchedLazyURLs)+4)
	for u := range baseMeta.fetchedLazyURLs {
		fetchedSet[u] = true
	}
	for _, rawURL := range candidates {
		u, err := ValidateStaticAssetURL(rawURL, d.allowLocalhost)
		if err != nil {
			continue
		}
		clean := u.String()
		if queuedSet[clean] {
			continue
		}
		queuedSet[clean] = true
		sc := scoreMap[clean]
		if sc == 0 {
			sc = scoreLazyChunkName(opName, urlBaseName(clean))
		}
		queue = append(queue, lazyQueueItem{url: clean, score: sc})
	}

	discoveredOps := make(map[string]GraphQLOperationSpec)
	var fetchedURLs []string
	fetchedCount := 0

	for idx := 0; idx < len(queue); idx++ {
		if fetchedCount >= maxSpecificLazyChunksPerOp {
			break
		}
		item := queue[idx]
		if fetchedCount >= maxLazyChunksPerOperation && item.score < highConfidenceLazyScore {
			continue
		}
		cleanURL := item.url
		if fetchedSet[cleanURL] {
			continue
		}
		fetchedSet[cleanURL] = true
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

		bodyStr := string(body)
		chunkSpecs := parseLazyChunkOperations(bodyStr, baseMeta.featureDefaults)
		for k, spec := range chunkSpecs {
			if _, already := baseMeta.Operations[k]; !already {
				discoveredOps[k] = spec
			}
		}
		if spec, ok := discoveredOps[opName]; ok && spec.QueryID != "" {
			break
		}

		// Follow direct child chunk references (e.g. route wrapper calling n.e(chunkId) or
		// dynamic import("./...")) within the bounded high-confidence budget.
		// For 2-hop Lists discovery (bundle.UserLists / loader.ListHandler -> shared~* child
		// chunk, DEC-093), prioritize direct children ahead of sibling Hop-1 wrappers so all
		// Lists operations are discovered in at most 2 static JS fetches.
		if fetchedCount < maxSpecificLazyChunksPerOp && item.score >= highConfidenceLazyScore {
			childURLs := extractChildChunkURLsFromLazyJS(bodyStr, cleanURL, baseMeta.chunkURLByID)
			var newChildren []lazyQueueItem
			for _, rawChild := range childURLs {
				cu, cErr := ValidateStaticAssetURL(rawChild, d.allowLocalhost)
				if cErr != nil {
					continue
				}
				cleanChild := cu.String()
				if queuedSet[cleanChild] || fetchedSet[cleanChild] {
					continue
				}
				queuedSet[cleanChild] = true
				cScore := 92
				if IsListsOperation(opName) && item.score >= 100 {
					childBase := strings.ToLower(urlBaseName(cleanChild))
					if strings.HasPrefix(childBase, "shared~") {
						cScore = item.score + 6
					} else {
						cScore = item.score + 2
					}
				}
				newChildren = append(newChildren, lazyQueueItem{url: cleanChild, score: cScore})
			}
			if len(newChildren) > 0 {
				sort.SliceStable(newChildren, func(i, j int) bool {
					return newChildren[i].score > newChildren[j].score
				})
				topScore := newChildren[0].score
				insertAt := len(queue)
				for j := idx + 1; j < len(queue); j++ {
					if queue[j].score < topScore {
						insertAt = j
						break
					}
				}
				updated := make([]lazyQueueItem, 0, len(queue)+len(newChildren))
				updated = append(updated, queue[:insertAt]...)
				updated = append(updated, newChildren...)
				updated = append(updated, queue[insertAt:]...)
				queue = updated
			}
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
	mainLazyRes := extractLazyChunksFromMainJS(
		string(mainBody),
		mainURL,
		parsedHome.chunkURLByID,
		parsedHome.opChunkIDs,
	)
	lazyURLs, lazyScores := filterAndMergeLazyChunkResults(
		d.allowLocalhost,
		lazyChunkExtractionResult{
			urls:   parsedHome.LazyChunkURLs,
			scores: parsedHome.lazyChunkScores,
		},
		mainLazyRes,
	)
	mergedChunkMap := filterAndMergeChunkIDMap(
		d.allowLocalhost,
		parsedHome.chunkURLByID,
		mainLazyRes.chunkURLByID,
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
		lazyChunkScores: lazyScores,
		chunkURLByID:    mergedChunkMap,
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

	reWebpackOndemandDirect   = regexp.MustCompile(`["']ondemand\.s["']\s*:\s*["']([A-Za-z0-9_.-]{4,128})["']`)
	reWebpackOndemandNumID    = regexp.MustCompile(`(?:\b|["'])(\d+)["']?\s*:\s*["']ondemand\.s["']`)
	reWebpackChunkNumID       = regexp.MustCompile(`(?:\b|["'])(\d{1,8})["']?\s*:\s*["']([A-Za-z0-9_.~+-]{2,512})["']`)
	reWebpackTernaryName1     = regexp.MustCompile(`\b(\d{1,8})\s*===?\s*[A-Za-z_$][A-Za-z0-9_$]*\s*\?\s*["']([A-Za-z0-9_.~+-]{2,512})["']`)
	reWebpackTernaryName2     = regexp.MustCompile(`\b[A-Za-z_$][A-Za-z0-9_$]*\s*===?\s*(\d{1,8})\s*\?\s*["']([A-Za-z0-9_.~+-]{2,512})["']`)
	reWebpackNumericHashEntry = regexp.MustCompile(`(?:\b|["'])(\d{1,8})["']?\s*:\s*["']([A-Za-z0-9_.-]{4,128})["']`)
	reWebpackChunkDirect      = regexp.MustCompile(`(?:["']([A-Za-z0-9_.~+-]{2,512})["']|(?:[{,]\s*)([A-Za-z_$][A-Za-z0-9_$]{2,120}))\s*:\s*["']([A-Za-z0-9_./~-]{4,256})["']`)
	reWebpackRuntimeSuffix    = regexp.MustCompile(`\}\s*\[[^\]]{1,32}\]\s*\+\s*["']([A-Za-z0-9_.-]*\.js)["']`)
	reWebpackHexHash          = regexp.MustCompile(`^[0-9a-f]{6,32}$`)
	reWebpackHexHashWithA     = regexp.MustCompile(`^[0-9a-f]{6,31}a?$`)
	reWebpackSimpleHash       = regexp.MustCompile(`^[A-Za-z0-9_-]{4,64}$`)
	reWebpackEnsureCall       = regexp.MustCompile(`\b[A-Za-z_$][A-Za-z0-9_$]*\.e\(\s*(?:(\d{1,8})|["']([A-Za-z0-9_.~+-]{2,512})["'])\s*\)`)
	reWebpackPromiseAll       = regexp.MustCompile(`Promise\.all\(\s*\[([^\]]+)\]\s*\)`)
	reBookmarksRoutePath      = regexp.MustCompile(`["'](?:/i/bookmarks|/bookmarks)["']`)
	reLikesRoutePath          = regexp.MustCompile(`["'](?:/i/user/[^"']{1,64}/likes|/:[A-Za-z0-9_]+/likes)["']`)
	reListsRoutePath          = regexp.MustCompile(`["'](?:/i/lists(?:/[^"']{0,64})?|/:[A-Za-z0-9_]+/lists|/lists)["']`)
	reInitialStateMarker      = regexp.MustCompile(`__INITIAL_STATE__\s*=\s*\{`)
	reInlineScriptBlock       = regexp.MustCompile(`(?is)<script\b([^>]*)>(.*?)</script>`)
	reScriptCloseTag          = regexp.MustCompile(`(?i)</script\b`)
	reNoModuleAttr            = regexp.MustCompile(`(?i)\snomodule(?:\s*=|\s|>|/)`)
	reInlineFullMainURL       = regexp.MustCompile(`https?://[^\s"'` + "`" + `<>)]+/main[.-][A-Za-z0-9_-]+\.js`)
	reInlineFullOndemandURL   = regexp.MustCompile(`https?://[^\s"'` + "`" + `<>)]+/ondemand\.s[.-][A-Za-z0-9_-]+\.js`)
	reRelativeOndemandRef     = regexp.MustCompile(`["']((?:\./|\.\./|/responsive-web/|assets/|chunks/|client-web/)?[A-Za-z0-9_./-]*ondemand\.s[.-][A-Za-z0-9_-]+\.js)["']`)
	reInlineJSAssetURL        = regexp.MustCompile(`https?://[^\s"'` + "`" + `<>)]+/[A-Za-z0-9_.~+-]+\.js`)
	reRelativeJSAssetRef      = regexp.MustCompile(`["']((?:\./|\.\./|/responsive-web/|assets/|chunks/|client-web/)[A-Za-z0-9_.~+/-]+\.js)["']`)
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
	htmlLazyRes := extractLazyChunksFromHTML(html, out.MainJSURL, tagURLs)
	out.LazyChunkURLs = htmlLazyRes.urls
	out.lazyChunkScores = htmlLazyRes.scores
	out.chunkURLByID = htmlLazyRes.chunkURLByID
	out.opChunkIDs = htmlLazyRes.opChunkIDs
	if out.OndemandJSURL == "" && htmlLazyRes.ondemandURL != "" {
		out.OndemandJSURL = htmlLazyRes.ondemandURL
	}

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

	// Vite relative or root-relative import/reference inside inline <script type="module">.
	if m := reRelativeOndemandRef.FindStringSubmatch(html); len(m) == 2 {
		if resolved := resolveRelativeAssetURL(mainJSURL, m[1]); resolved != "" {
			return resolved
		}
	}

	// Scan each inline <script> block independently so chunk IDs never bleed across </script>.
	for _, block := range extractNonLegacyInlineScripts(html) {
		bm := parseWebpackBlockManifest(block, mainJSURL)
		if bm.ondemandURL != "" {
			return bm.ondemandURL
		}
	}

	// Fallback for synthetic snippets without enclosing </script>.
	if m := reWebpackOndemandDirect.FindStringSubmatch(html); len(m) == 2 {
		return resolveWebpackChunkURLWithSuffix(mainJSURL, "ondemand.s", m[1], "a.js")
	}
	if u := extractWebpackNumericOndemandURL(html, mainJSURL, true); u != "" {
		return u
	}

	return ""
}

func extractWebpackNumericOndemandURL(src, mainJSURL string, stopAtScriptClose bool) string {
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
		if stopAtScriptClose {
			if closeLoc := reScriptCloseTag.FindStringIndex(tail); closeLoc != nil {
				tail = tail[:closeLoc[0]]
			}
		}
		hashRe := regexp.MustCompile(`(?:\b|["'])` + regexp.QuoteMeta(chunkID) + `["']?\s*:\s*["']([A-Za-z0-9_.-]{4,64})["']`)
		for _, hm := range hashRe.FindAllStringSubmatch(tail, -1) {
			if len(hm) != 2 || hm[1] == "ondemand.s" {
				continue
			}
			if reWebpackHexHash.MatchString(hm[1]) {
				return resolveWebpackChunkURLWithSuffix(mainJSURL, "ondemand.s", hm[1], "a.js")
			}
			if fallbackURL == "" {
				fallbackURL = resolveWebpackChunkURLWithSuffix(mainJSURL, "ondemand.s", hm[1], "a.js")
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
		mainJSURL := mainURL.String()
		if m := reRelativeOndemandRef.FindStringSubmatch(mainJS); len(m) == 2 {
			if resolved := resolveRelativeAssetURL(mainJSURL, m[1]); resolved != "" {
				return resolved
			}
		}
		bm := parseWebpackBlockManifest(mainJS, mainJSURL)
		if bm.ondemandURL != "" {
			return bm.ondemandURL
		}
		if m := reWebpackOndemandDirect.FindStringSubmatch(mainJS); len(m) == 2 {
			return resolveWebpackChunkURLWithSuffix(mainJSURL, "ondemand.s", m[1], "a.js")
		}
		if u := extractWebpackNumericOndemandURL(mainJS, mainJSURL, false); u != "" {
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

type lazyChunkExtractionResult struct {
	urls         map[string][]string
	scores       map[string]map[string]int
	chunkURLByID map[string]string
	opChunkIDs   map[string]map[string]bool
	ondemandURL  string
}

type webpackRuntimeTableSpan struct {
	start  int
	end    int
	suffix string
}

type webpackBlockManifest struct {
	ondemandURL  string
	namedChunks  []webpackNamedChunkURL
	chunkURLByID map[string]string
	opChunkIDs   map[string]map[string]bool
}

type webpackNamedChunkURL struct {
	chunkID   string
	chunkName string
	url       string
}

func extractNonLegacyInlineScripts(html string) []string {
	matches := reInlineScriptBlock.FindAllStringSubmatch(html, -1)
	if len(matches) == 0 {
		return nil
	}
	blocks := make([]string, 0, len(matches))
	for _, m := range matches {
		if len(m) != 3 {
			continue
		}
		openAttrs, body := m[1], m[2]
		if strings.TrimSpace(body) == "" || reNoModuleAttr.MatchString("<script "+openAttrs+">") {
			continue
		}
		attrs := parseHTMLAttrs("<script " + openAttrs + ">")
		if src := strings.TrimSpace(attrs["src"]); src != "" && isLegacyAssetURL(src) {
			continue
		}
		blocks = append(blocks, body)
	}
	return blocks
}

func findInitialStateSpans(src string) [][2]int {
	locs := reInitialStateMarker.FindAllStringIndex(src, -1)
	if len(locs) == 0 {
		return nil
	}
	var spans [][2]int
	for _, loc := range locs {
		bracePos := loc[1] - 1
		if endPos := findMatchingCloseBrace(src, bracePos, len(src)); endPos > bracePos {
			spans = append(spans, [2]int{bracePos, endPos})
		}
	}
	return spans
}

func posInSpans(pos int, spans [][2]int) bool {
	for _, sp := range spans {
		if pos >= sp[0] && pos <= sp[1] {
			return true
		}
	}
	return false
}

func findRuntimeHashTableSpans(src string) []webpackRuntimeTableSpan {
	locs := reWebpackRuntimeSuffix.FindAllStringSubmatchIndex(src, -1)
	if len(locs) == 0 {
		return nil
	}
	var spans []webpackRuntimeTableSpan
	for _, loc := range locs {
		if len(loc) < 4 || loc[2] < 0 || loc[3] < 0 {
			continue
		}
		closeBracePos := loc[0]
		suffix := src[loc[2]:loc[3]]
		openBracePos := findEnclosingBraceStartBounded(src, closeBracePos, len(src))
		if openBracePos >= 0 && openBracePos < closeBracePos {
			spans = append(spans, webpackRuntimeTableSpan{
				start:  openBracePos,
				end:    closeBracePos,
				suffix: suffix,
			})
		}
	}
	return spans
}

func suffixForPos(pos int, runtimeSpans []webpackRuntimeTableSpan) (string, bool) {
	for _, sp := range runtimeSpans {
		if pos >= sp.start && pos <= sp.end {
			return sp.suffix, true
		}
	}
	return "a.js", false
}

func parseWebpackBlockManifest(src, mainJSURL string) webpackBlockManifest {
	var out webpackBlockManifest
	if strings.TrimSpace(src) == "" {
		return out
	}

	initialStateSpans := findInitialStateSpans(src)
	runtimeSpans := findRuntimeHashTableSpans(src)
	out.chunkURLByID = make(map[string]string)
	out.opChunkIDs = make(map[string]map[string]bool)

	recordOpChunkID := func(chunkID, chunkName string) {
		if chunkID == "" {
			return
		}
		for _, opName := range OptionalOperations() {
			if scoreLazyChunkName(opName, chunkName) >= highConfidenceLazyScore {
				if out.opChunkIDs[opName] == nil {
					out.opChunkIDs[opName] = make(map[string]bool)
				}
				out.opChunkIDs[opName][chunkID] = true
			}
		}
	}

	// 1. Direct name -> hash / filename mappings (both quoted and unquoted keys).
	for _, loc := range reWebpackChunkDirect.FindAllStringSubmatchIndex(src, -1) {
		if len(loc) < 8 || posInSpans(loc[0], initialStateSpans) {
			continue
		}
		var chunkName string
		quotedKey := false
		if loc[2] >= 0 && loc[3] >= 0 {
			chunkName = src[loc[2]:loc[3]]
			quotedKey = true
		} else if loc[4] >= 0 && loc[5] >= 0 {
			chunkName = src[loc[4]:loc[5]]
		}
		val := src[loc[6]:loc[7]]
		if !isSafeWebpackChunkName(chunkName) {
			continue
		}
		suffix, inRuntime := suffixForPos(loc[0], runtimeSpans)

		if chunkName == "ondemand.s" {
			resolved := resolveWebpackDirectValue(mainJSURL, chunkName, val, suffix)
			if resolved != "" && out.ondemandURL == "" {
				out.ondemandURL = resolved
			}
			continue
		}

		isExplicit := isExplicitWebpackChunkName(chunkName)
		isBareOpt := matchesAnyOptionalChunk(chunkName)
		if !isExplicit && !isBareOpt {
			continue
		}
		// Require hex hash or .js suffix or confirmed runtime map for unquoted/bare keys
		// (prevents matching incidental JS properties like `{Bookmarks: "Timeline"}`).
		isJSPath := strings.HasSuffix(strings.ToLower(val), ".js")
		if !isJSPath && !reWebpackSimpleHash.MatchString(val) {
			continue
		}
		if (!quotedKey || !isExplicit || strings.EqualFold(chunkName, "api")) &&
			!isJSPath && !inRuntime && !reWebpackHexHashWithA.MatchString(val) {
			continue
		}

		resolved := resolveWebpackDirectValue(mainJSURL, chunkName, val, suffix)
		if resolved == "" {
			continue
		}
		if len(out.chunkURLByID) < maxRetainedManifestChunks {
			out.chunkURLByID[chunkName] = resolved
		}
		recordOpChunkID(chunkName, chunkName)
		if isBareOpt {
			out.namedChunks = append(out.namedChunks, webpackNamedChunkURL{
				chunkID:   chunkName,
				chunkName: chunkName,
				url:       resolved,
			})
		}
	}

	// 2. Collect numeric chunkID -> chunkName entries (both object map and ternary expressions).
	type nameEntry struct {
		chunkID   string
		chunkName string
		endPos    int
	}
	var nameEntries []nameEntry
	nameByID := make(map[string]string)

	addNameEntry := func(chunkID, chunkName string, startPos, endPos int) {
		if posInSpans(startPos, initialStateSpans) || !isSafeWebpackChunkName(chunkName) {
			return
		}
		if reWebpackHexHashWithA.MatchString(chunkName) && !matchesAnyOptionalChunk(chunkName) {
			return
		}
		if chunkName != "ondemand.s" && !isExplicitWebpackChunkName(chunkName) && !matchesAnyOptionalChunk(chunkName) {
			return
		}
		if _, exists := nameByID[chunkID]; !exists {
			nameByID[chunkID] = chunkName
		}
		nameEntries = append(nameEntries, nameEntry{
			chunkID:   chunkID,
			chunkName: chunkName,
			endPos:    endPos,
		})
	}

	for _, loc := range reWebpackChunkNumID.FindAllStringSubmatchIndex(src, -1) {
		if len(loc) >= 6 && loc[2] >= 0 && loc[4] >= 0 {
			addNameEntry(src[loc[2]:loc[3]], src[loc[4]:loc[5]], loc[0], loc[1])
		}
	}
	for _, re := range []*regexp.Regexp{reWebpackTernaryName1, reWebpackTernaryName2} {
		for _, loc := range re.FindAllStringSubmatchIndex(src, -1) {
			if len(loc) >= 6 && loc[2] >= 0 && loc[4] >= 0 {
				addNameEntry(src[loc[2]:loc[3]], src[loc[4]:loc[5]], loc[0], loc[1])
			}
		}
	}

	// 3. Collect all numeric chunkID -> value candidates in this script block (linear O(N) pass).
	type hashEntry struct {
		val       string
		startPos  int
		suffix    string
		inRuntime bool
	}
	hashesByID := make(map[string][]hashEntry)
	allHashLocs := reWebpackNumericHashEntry.FindAllStringSubmatchIndex(src, -1)
	for _, loc := range allHashLocs {
		if len(loc) < 6 || loc[2] < 0 || loc[4] < 0 || posInSpans(loc[0], initialStateSpans) {
			continue
		}
		cid := src[loc[2]:loc[3]]
		val := src[loc[4]:loc[5]]
		suffix, inRuntime := suffixForPos(loc[0], runtimeSpans)
		hashesByID[cid] = append(hashesByID[cid], hashEntry{
			val:       val,
			startPos:  loc[0],
			suffix:    suffix,
			inRuntime: inRuntime,
		})

		// Unnamed numeric chunks inside a confirmed runtime hash table `{...}[e]+"a.js"`
		// resolve to `<chunkID>.<hash><suffix>` so Promise.all / route loaders can reach them.
		if inRuntime && len(out.chunkURLByID) < maxRetainedManifestChunks {
			if _, hasName := nameByID[cid]; !hasName && reWebpackHexHashWithA.MatchString(val) {
				out.chunkURLByID[cid] = resolveWebpackDirectValue(mainJSURL, cid, val, suffix)
			}
		}
	}

	// Resolve each named chunkID against hashesByID (preferring hex hashes after nameEntry,
	// then hex hashes before nameEntry in the same script, then non-hex synthetic tokens).
	confirmedHashBraceStarts := make(map[int]string)
	for _, ne := range nameEntries {
		list := hashesByID[ne.chunkID]
		if len(list) == 0 {
			continue
		}
		var hexAfter, hexBefore, otherAfter, otherBefore *hashEntry
		for i := range list {
			he := &list[i]
			if he.val == ne.chunkName {
				continue
			}
			isHex := reWebpackHexHash.MatchString(he.val) || (he.suffix == ".js" && reWebpackHexHashWithA.MatchString(he.val))
			if he.startPos >= ne.endPos {
				if isHex && hexAfter == nil {
					hexAfter = he
				} else if !isHex && otherAfter == nil && reWebpackSimpleHash.MatchString(he.val) {
					otherAfter = he
				}
			} else {
				if isHex && hexBefore == nil {
					hexBefore = he
				} else if !isHex && otherBefore == nil && reWebpackSimpleHash.MatchString(he.val) {
					otherBefore = he
				}
			}
		}
		var chosen *hashEntry
		switch {
		case hexAfter != nil:
			chosen = hexAfter
		case hexBefore != nil:
			chosen = hexBefore
		case otherAfter != nil:
			chosen = otherAfter
		case otherBefore != nil:
			chosen = otherBefore
		}
		if chosen == nil {
			continue
		}
		resolved := resolveWebpackDirectValue(mainJSURL, ne.chunkName, chosen.val, chosen.suffix)
		if resolved == "" {
			continue
		}
		if !chosen.inRuntime {
			if openPos := findEnclosingBraceStartBounded(src, chosen.startPos, 256*1024); openPos >= 0 {
				confirmedHashBraceStarts[openPos] = chosen.suffix
			}
		}
		if ne.chunkName == "ondemand.s" {
			if out.ondemandURL == "" {
				out.ondemandURL = resolved
			}
			continue
		}
		if len(out.chunkURLByID) < maxRetainedManifestChunks {
			out.chunkURLByID[ne.chunkID] = resolved
		}
		recordOpChunkID(ne.chunkID, ne.chunkName)
		if matchesAnyOptionalChunk(ne.chunkName) {
			out.namedChunks = append(out.namedChunks, webpackNamedChunkURL{
				chunkID:   ne.chunkID,
				chunkName: ne.chunkName,
				url:       resolved,
			})
		}
	}

	// Also register unnamed numeric hex entries from any hash object that resolved at least
	// one named chunk (e.g. standalone hash tables assigned to a variable).
	if len(confirmedHashBraceStarts) > 0 && len(out.chunkURLByID) < maxRetainedManifestChunks {
		type spanWithSuffix struct {
			start  int
			end    int
			suffix string
		}
		var confirmedSpans []spanWithSuffix
		for openPos, sfx := range confirmedHashBraceStarts {
			if endPos := findMatchingCloseBrace(src, openPos, len(src)); endPos > openPos {
				confirmedSpans = append(confirmedSpans, spanWithSuffix{start: openPos, end: endPos, suffix: sfx})
			}
		}
		for cid, list := range hashesByID {
			if len(out.chunkURLByID) >= maxRetainedManifestChunks {
				break
			}
			if _, exists := out.chunkURLByID[cid]; exists {
				continue
			}
			if _, hasName := nameByID[cid]; hasName {
				continue
			}
			for _, he := range list {
				if !reWebpackHexHashWithA.MatchString(he.val) {
					continue
				}
				for _, sp := range confirmedSpans {
					if he.startPos >= sp.start && he.startPos <= sp.end {
						out.chunkURLByID[cid] = resolveWebpackDirectValue(mainJSURL, cid, he.val, sp.suffix)
						break
					}
				}
				if _, ok := out.chunkURLByID[cid]; ok {
					break
				}
			}
		}
	}

	return out
}

func resolveWebpackDirectValue(mainJSURL, chunkName, val, suffix string) string {
	trimmed := strings.TrimSpace(val)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "https://") || strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "//") {
		return normalizeProtocolRelativeURL(trimmed)
	}
	if strings.HasSuffix(strings.ToLower(trimmed), ".js") {
		if strings.Contains(trimmed, "/") {
			return resolveRelativeAssetURL(mainJSURL, trimmed)
		}
		return resolveRelativeAssetURL(mainJSURL, "./"+trimmed)
	}
	return resolveWebpackChunkURLWithSuffix(mainJSURL, chunkName, trimmed, suffix)
}

func extractLazyChunksFromHTML(html, mainJSURL string, tagURLs []string) lazyChunkExtractionResult {
	var res lazyChunkExtractionResult
	res.chunkURLByID = make(map[string]string)
	res.opChunkIDs = make(map[string]map[string]bool)

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

	// 2. Full inline JS URLs and relative JS references (e.g. Vite modulepreload/import/__vite__mapDeps).
	for _, u := range reInlineJSAssetURL.FindAllString(html, -1) {
		addCand(u, urlBaseName(u))
	}
	for _, m := range reRelativeJSAssetRef.FindAllStringSubmatch(html, -1) {
		if len(m) == 2 {
			if resolved := resolveRelativeAssetURL(mainJSURL, m[1]); resolved != "" {
				addCand(resolved, urlBaseName(resolved))
			}
		}
	}

	// 3. Per-<script> webpack manifests (isolated per inline script so IDs never bleed across </script>).
	blocks := extractNonLegacyInlineScripts(html)
	if len(blocks) == 0 && strings.TrimSpace(html) != "" {
		blocks = []string{html}
	}
	for _, block := range blocks {
		bm := parseWebpackBlockManifest(block, mainJSURL)
		if res.ondemandURL == "" && bm.ondemandURL != "" {
			res.ondemandURL = bm.ondemandURL
		}
		for _, nc := range bm.namedChunks {
			addCand(nc.url, nc.chunkName)
		}
		for k, v := range bm.chunkURLByID {
			if len(res.chunkURLByID) >= maxRetainedManifestChunks {
				break
			}
			if _, exists := res.chunkURLByID[k]; !exists {
				res.chunkURLByID[k] = v
			}
		}
		for opName, ids := range bm.opChunkIDs {
			if res.opChunkIDs[opName] == nil {
				res.opChunkIDs[opName] = make(map[string]bool)
			}
			for id := range ids {
				res.opChunkIDs[opName][id] = true
			}
		}
	}

	res.urls, res.scores = rankLazyChunkMapWithScores(perOp)
	return res
}

func extractLazyChunksFromMainJS(
	mainJS string,
	mainURL *url.URL,
	homeChunkURLByID map[string]string,
	homeOpChunkIDs map[string]map[string]bool,
) lazyChunkExtractionResult {
	var res lazyChunkExtractionResult
	if mainJS == "" || mainURL == nil {
		return res
	}
	mainJSURL := mainURL.String()
	perOp := make(map[string][]scoredLazyChunk)
	order := 0
	addExplicit := func(opName, rawURL, chunkName string, score int) {
		if rawURL == "" || score <= 0 || isLegacyAssetURL(rawURL) || isIgnoredChunkName(chunkName) {
			return
		}
		order++
		perOp[opName] = append(perOp[opName], scoredLazyChunk{
			url:       rawURL,
			chunkName: chunkName,
			score:     score,
			order:     order,
		})
	}
	addCand := func(rawURL, chunkName string) {
		if rawURL == "" || isLegacyAssetURL(rawURL) {
			return
		}
		for _, opName := range OptionalOperations() {
			sc := scoreLazyChunkName(opName, chunkName)
			if sc > 0 {
				addExplicit(opName, rawURL, chunkName, sc)
			}
		}
	}

	for _, u := range reInlineJSAssetURL.FindAllString(mainJS, -1) {
		addCand(u, urlBaseName(u))
	}
	for _, m := range reRelativeJSAssetRef.FindAllStringSubmatch(mainJS, -1) {
		if len(m) == 2 {
			if resolved := resolveRelativeAssetURL(mainJSURL, m[1]); resolved != "" {
				addCand(resolved, urlBaseName(resolved))
			}
		}
	}

	bm := parseWebpackBlockManifest(mainJS, mainJSURL)
	res.ondemandURL = bm.ondemandURL
	res.chunkURLByID = bm.chunkURLByID
	for _, nc := range bm.namedChunks {
		addCand(nc.url, nc.chunkName)
	}

	combinedChunkURLs := make(map[string]string, len(homeChunkURLByID)+len(bm.chunkURLByID))
	for k, v := range homeChunkURLByID {
		combinedChunkURLs[k] = v
	}
	for k, v := range bm.chunkURLByID {
		if _, exists := combinedChunkURLs[k]; !exists {
			combinedChunkURLs[k] = v
		}
	}

	combinedOpIDs := make(map[string]map[string]bool)
	for _, opName := range OptionalOperations() {
		combinedOpIDs[opName] = make(map[string]bool)
		for id := range homeOpChunkIDs[opName] {
			combinedOpIDs[opName][id] = true
		}
		for id := range bm.opChunkIDs[opName] {
			combinedOpIDs[opName][id] = true
		}
	}

	// 1. Co-loaded Promise.all([n.e(a), n.e(b), ...]) sibling chunks in main.*.js:
	// when a route loads bundle.Bookmarks alongside an unnamed/shared split chunk (e.g. 4812.<hash>a.js),
	// link that sibling chunk directly to OpBookmarks (and symmetrically for OpLikes).
	if len(combinedChunkURLs) > 0 {
		for _, m := range reWebpackPromiseAll.FindAllStringSubmatch(mainJS, -1) {
			if len(m) != 2 {
				continue
			}
			ids := extractEnsureChunkIDs(m[1])
			if len(ids) < 2 {
				continue
			}
			for _, opName := range OptionalOperations() {
				matchedOp := false
				for _, cid := range ids {
					if combinedOpIDs[opName][cid] || scoreLazyChunkName(opName, cid) >= highConfidenceLazyScore {
						matchedOp = true
						break
					}
				}
				if !matchedOp {
					continue
				}
				for _, cid := range ids {
					if combinedOpIDs[opName][cid] {
						continue
					}
					if u := combinedChunkURLs[cid]; u != "" {
						base := urlBaseName(u)
						if !isIgnoredChunkName(base) {
							addExplicit(opName, u, base, 96)
						}
					}
				}
			}
		}
	}

	// 2. Route-definition discovery in main.*.js (e.g. path:"/i/bookmarks" -> loader n.e(...) or import(...))
	// so even 100% numeric unnamed chunk builds locate the exact route bundles.
	extractRouteLinkedChunks(mainJS, mainJSURL, reBookmarksRoutePath, OpBookmarks, combinedChunkURLs, addExplicit)
	extractRouteLinkedChunks(mainJS, mainJSURL, reLikesRoutePath, OpLikes, combinedChunkURLs, addExplicit)
	for _, listOp := range ListsOperations() {
		extractRouteLinkedChunks(mainJS, mainJSURL, reListsRoutePath, listOp, combinedChunkURLs, addExplicit)
	}

	res.urls, res.scores = rankLazyChunkMapWithScores(perOp)
	return res
}

func extractRouteLinkedChunks(
	mainJS, mainJSURL string,
	routeRe *regexp.Regexp,
	opName string,
	chunkURLByID map[string]string,
	addExplicit func(opName, rawURL, chunkName string, score int),
) {
	locs := routeRe.FindAllStringIndex(mainJS, -1)
	for _, loc := range locs {
		win, ok := extractEnclosingJSObject(mainJS, loc[0])
		if !ok || len(win) > 8192 {
			start := max(0, loc[0]-400)
			end := min(len(mainJS), loc[1]+800)
			win = mainJS[start:end]
		}
		for _, cid := range extractEnsureChunkIDs(win) {
			if u := chunkURLByID[cid]; u != "" {
				base := urlBaseName(u)
				if !isIgnoredChunkName(base) {
					addExplicit(opName, u, base, 94)
				}
			}
		}
		for _, u := range reInlineJSAssetURL.FindAllString(win, -1) {
			base := urlBaseName(u)
			if !isIgnoredChunkName(base) {
				addExplicit(opName, u, base, 94)
			}
		}
		for _, m := range reRelativeJSAssetRef.FindAllStringSubmatch(win, -1) {
			if len(m) == 2 {
				if resolved := resolveRelativeAssetURL(mainJSURL, m[1]); resolved != "" {
					base := urlBaseName(resolved)
					if !isIgnoredChunkName(base) {
						addExplicit(opName, uOrEmpty(resolved), base, 94)
					}
				}
			}
		}
	}
}

func uOrEmpty(s string) string { return s }

func extractEnsureChunkIDs(snippet string) []string {
	matches := reWebpackEnsureCall.FindAllStringSubmatch(snippet, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(matches))
	ids := make([]string, 0, len(matches))
	for _, m := range matches {
		cid := m[1]
		if cid == "" {
			cid = m[2]
		}
		if cid == "" || seen[cid] {
			continue
		}
		seen[cid] = true
		ids = append(ids, cid)
	}
	return ids
}

func extractChildChunkURLsFromLazyJS(lazyJS, lazyURL string, chunkURLByID map[string]string) []string {
	if lazyJS == "" {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	push := func(u string) {
		if u == "" || isLegacyAssetURL(u) || seen[u] {
			return
		}
		base := urlBaseName(u)
		if isIgnoredChunkName(base) {
			return
		}
		seen[u] = true
		out = append(out, u)
	}

	if len(chunkURLByID) > 0 {
		for _, cid := range extractEnsureChunkIDs(lazyJS) {
			if u := chunkURLByID[cid]; u != "" {
				push(u)
			}
		}
	}
	for _, u := range reInlineJSAssetURL.FindAllString(lazyJS, -1) {
		push(u)
	}
	for _, m := range reRelativeJSAssetRef.FindAllStringSubmatch(lazyJS, -1) {
		if len(m) == 2 {
			if resolved := resolveRelativeAssetURL(lazyURL, m[1]); resolved != "" {
				push(resolved)
			}
		}
	}
	return out
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
		strings.HasPrefix(lower, "vendors~") ||
		strings.HasPrefix(lower, "default~") ||
		strings.HasPrefix(lower, "loader.") ||
		strings.HasPrefix(lower, "ondemand.") ||
		strings.HasPrefix(lower, "chunk.") ||
		strings.HasPrefix(lower, "route.") ||
		strings.HasPrefix(lower, "page.") ||
		strings.HasPrefix(lower, "modules.") ||
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

func isIgnoredChunkName(chunkName string) bool {
	name := strings.ToLower(strings.TrimSpace(chunkName))
	if name == "" || strings.Contains(name, "..") || strings.ContainsAny(name, " \t\r\n?#\\") {
		return true
	}
	name = strings.TrimSuffix(name, ".js")
	return name == "" ||
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
		strings.HasPrefix(name, "polyfills")
}

func scoreLazyChunkName(opName, chunkName string) int {
	if isIgnoredChunkName(chunkName) {
		return 0
	}
	name := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(chunkName)), ".js")

	switch opName {
	case OpBookmarks:
		switch {
		case name == "bundle.bookmarks" || strings.HasPrefix(name, "bundle.bookmarks.") || strings.HasPrefix(name, "bundle.bookmarks-") ||
			name == "bundle.bookmark" || strings.HasPrefix(name, "bundle.bookmark.") || strings.HasPrefix(name, "bundle.bookmark-"):
			return 115
		case strings.Contains(name, "loader.") && hasAnyChunkToken(name, "bookmark", "bookmarks", "bookmarktimeline", "bookmarkstimeline"):
			return 112
		case strings.Contains(name, "bundle.bookmarks") ||
			strings.Contains(name, "bundle.bookmark.") ||
			strings.Contains(name, "bundle.bookmark~") ||
			strings.HasSuffix(name, "bundle.bookmark"):
			return 108
		case name == "bookmarks" || strings.HasPrefix(name, "bookmarks.") || strings.HasPrefix(name, "bookmarks-") ||
			name == "bookmark" || strings.HasPrefix(name, "bookmark.") || strings.HasPrefix(name, "bookmark-") ||
			hasAnyChunkToken(name, "bookmarktimeline", "bookmarkstimeline"):
			return 102
		case hasAnyChunkToken(name, "bookmark", "bookmarks"):
			return 98
		case strings.Contains(name, "bookmark"):
			// Secondary bookmark chunks (such as bundle.BookmarkFolders) stay >= highConfidenceLazyScore
			// if needed, while ranking strictly behind primary Bookmark / loader / shared chunks.
			return 75
		case name == "endpoints" || strings.Contains(name, "endpoints") || strings.Contains(name, "graphql"):
			return 50
		case name == "api" || strings.HasPrefix(name, "api.") || strings.HasPrefix(name, "api-"):
			return 45
		}
	case OpLikes:
		switch {
		case name == "bundle.likes" || strings.HasPrefix(name, "bundle.likes.") || strings.HasPrefix(name, "bundle.likes-") ||
			name == "bundle.like" || strings.HasPrefix(name, "bundle.like.") || strings.HasPrefix(name, "bundle.like-"):
			return 115
		case strings.Contains(name, "loader.") && hasAnyChunkToken(name, "likes", "like", "likestimeline", "favoritestimeline"):
			return 112
		case strings.Contains(name, "bundle.likes") ||
			strings.Contains(name, "bundle.like.") ||
			strings.Contains(name, "bundle.like~") ||
			strings.HasSuffix(name, "bundle.like"):
			return 108
		case name == "likes" || strings.HasPrefix(name, "likes.") || strings.HasPrefix(name, "likes-") ||
			hasAnyChunkToken(name, "likestimeline", "favoritestimeline"):
			return 102
		case hasAnyChunkToken(name, "likes"):
			return 98
		case name == "bundle.profile" || strings.HasPrefix(name, "bundle.profile.") || strings.HasPrefix(name, "bundle.profile-") ||
			strings.Contains(name, "bundle.profile") ||
			(strings.Contains(name, "loader.") && hasAnyChunkToken(name, "profile")):
			return 75
		case name == "endpoints" || strings.Contains(name, "endpoints") || strings.Contains(name, "graphql"):
			return 50
		case name == "api" || strings.HasPrefix(name, "api.") || strings.HasPrefix(name, "api-"):
			return 45
		}
	case OpUserArticlesTweets:
		switch {
		case name == "bundle.articles" || strings.HasPrefix(name, "bundle.articles.") || strings.HasPrefix(name, "bundle.articles-") ||
			name == "bundle.article" || strings.HasPrefix(name, "bundle.article.") || strings.HasPrefix(name, "bundle.article-"):
			return 115
		case hasAnyChunkToken(name, "articles", "article"):
			return 98
		case name == "endpoints" || strings.Contains(name, "endpoints") || strings.Contains(name, "graphql"):
			return 50
		case name == "api" || strings.HasPrefix(name, "api.") || strings.HasPrefix(name, "api-"):
			return 45
		}
	case OpCombinedLists, OpListOwnerships, OpListByRestId, OpListLatestTweetsTimeline, OpListMembers:
		// DEC-093 verified 2-hop topology: bundle.UserLists / loader.ListHandler ->
		// shared~* child chunk containing all Lists GraphQL operations.
		switch {
		case name == "bundle.userlists" || strings.HasPrefix(name, "bundle.userlists.") || strings.HasPrefix(name, "bundle.userlists-"):
			return 120
		case name == "loader.listhandler" || strings.HasPrefix(name, "loader.listhandler.") || strings.HasPrefix(name, "loader.listhandler-"):
			return 118
		case name == "bundle.lists" || strings.HasPrefix(name, "bundle.lists.") || strings.HasPrefix(name, "bundle.lists-") ||
			name == "bundle.list" || strings.HasPrefix(name, "bundle.list.") || strings.HasPrefix(name, "bundle.list-"):
			return 115
		case strings.Contains(name, "loader.") && hasAnyChunkToken(name, "listhandler", "list", "lists", "userlists"):
			return 112
		case strings.Contains(name, "bundle.userlists") || strings.Contains(name, "loader.listhandler") ||
			strings.Contains(name, "bundle.lists") || strings.Contains(name, "bundle.list.") || strings.Contains(name, "bundle.list~"):
			return 108
		case hasAnyChunkToken(name, "userlists", "listhandler", "listdetail", "listmembers", "lists"):
			return 100
		case name == "bundle.routes" || strings.HasPrefix(name, "bundle.routes.") || strings.HasPrefix(name, "bundle.routes-"):
			return 85
		case name == "endpoints" || strings.Contains(name, "endpoints") || strings.Contains(name, "graphql"):
			return 50
		case name == "api" || strings.HasPrefix(name, "api.") || strings.HasPrefix(name, "api-"):
			return 45
		}
	}
	return 0
}

func containsChunkToken(name, token string) bool {
	return hasAnyChunkToken(name, token)
}

func hasAnyChunkToken(name string, tokens ...string) bool {
	parts := strings.FieldsFunc(name, func(r rune) bool {
		return r == '.' || r == '~' || r == '-' || r == '_' || r == '/'
	})
	for _, p := range parts {
		for _, want := range tokens {
			if p == want {
				return true
			}
		}
	}
	return false
}

func rankLazyChunkMapWithScores(perOp map[string][]scoredLazyChunk) (map[string][]string, map[string]map[string]int) {
	if len(perOp) == 0 {
		return nil, nil
	}
	outURLs := make(map[string][]string, len(perOp))
	outScores := make(map[string]map[string]int, len(perOp))
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
		scores := make(map[string]int, min(len(list), maxLazyCandidatesPerOp))
		urls := make([]string, 0, min(len(list), maxLazyCandidatesPerOp))
		for _, item := range list {
			if seen[item.url] {
				if item.score > scores[item.url] {
					scores[item.url] = item.score
				}
				continue
			}
			seen[item.url] = true
			scores[item.url] = item.score
			urls = append(urls, item.url)
			if len(urls) >= maxLazyCandidatesPerOp {
				break
			}
		}
		if len(urls) > 0 {
			outURLs[opName] = urls
			outScores[opName] = scores
		}
	}
	if len(outURLs) == 0 {
		return nil, nil
	}
	return outURLs, outScores
}

func filterAndMergeLazyChunkResults(
	allowLocalhost bool,
	results ...lazyChunkExtractionResult,
) (map[string][]string, map[string]map[string]int) {
	type mergedItem struct {
		url   string
		score int
		order int
	}
	outURLs := make(map[string][]string)
	outScores := make(map[string]map[string]int)
	for _, opName := range OptionalOperations() {
		byURL := make(map[string]*mergedItem)
		var items []*mergedItem
		order := 0
		for _, r := range results {
			for _, rawURL := range r.urls[opName] {
				u, err := ValidateStaticAssetURL(rawURL, allowLocalhost)
				if err != nil {
					continue
				}
				clean := u.String()
				sc := r.scores[opName][rawURL]
				if sc == 0 {
					sc = r.scores[opName][clean]
				}
				if sc == 0 {
					sc = scoreLazyChunkName(opName, urlBaseName(clean))
				}
				if existing, ok := byURL[clean]; ok {
					if sc > existing.score {
						existing.score = sc
					}
					continue
				}
				order++
				item := &mergedItem{url: clean, score: sc, order: order}
				byURL[clean] = item
				items = append(items, item)
			}
		}
		if len(items) == 0 {
			continue
		}
		sort.SliceStable(items, func(i, j int) bool {
			if items[i].score != items[j].score {
				return items[i].score > items[j].score
			}
			return items[i].order < items[j].order
		})
		if len(items) > maxLazyCandidatesPerOp {
			items = items[:maxLazyCandidatesPerOp]
		}
		urls := make([]string, 0, len(items))
		scores := make(map[string]int, len(items))
		for _, it := range items {
			urls = append(urls, it.url)
			scores[it.url] = it.score
		}
		outURLs[opName] = urls
		outScores[opName] = scores
	}
	if len(outURLs) == 0 {
		return nil, nil
	}
	return outURLs, outScores
}

func filterAndMergeChunkIDMap(allowLocalhost bool, maps ...map[string]string) map[string]string {
	out := make(map[string]string)
	for _, m := range maps {
		for k, rawURL := range m {
			if len(out) >= maxRetainedManifestChunks {
				return out
			}
			if _, exists := out[k]; exists {
				continue
			}
			u, err := ValidateStaticAssetURL(rawURL, allowLocalhost)
			if err != nil {
				continue
			}
			out[k] = u.String()
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func resolveRelativeAssetURL(baseJSURL, relRef string) string {
	trimmed := strings.TrimSpace(relRef)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(trimmed, "https://") || strings.HasPrefix(trimmed, "http://") || strings.HasPrefix(trimmed, "//") {
		return normalizeProtocolRelativeURL(trimmed)
	}
	baseU, err := url.Parse(baseJSURL)
	if err != nil {
		return ""
	}
	refU, err := url.Parse(trimmed)
	if err != nil {
		return ""
	}
	// Handle Vite __vite__mapDeps / webpack publicPath-relative paths like
	// "assets/Bookmarks-xxx.js", "chunks/Bookmarks-xxx.js", or "client-web/bundle.Bookmarks.js"
	// when baseJSURL already sits inside "/assets/" or "/client-web/".
	if !strings.HasPrefix(trimmed, ".") && !strings.HasPrefix(trimmed, "/") {
		if slash := strings.IndexByte(trimmed, '/'); slash > 0 {
			firstSeg := trimmed[:slash]
			baseDir := path.Dir(baseU.Path)
			lastDirSeg := path.Base(baseDir)
			if lastDirSeg == firstSeg || (lastDirSeg == "assets" && firstSeg == "chunks") {
				parentCopy := *baseU
				parentCopy.Path = strings.TrimSuffix(path.Dir(baseDir), "/") + "/"
				parentCopy.RawQuery = ""
				parentCopy.Fragment = ""
				return parentCopy.ResolveReference(refU).String()
			}
		}
	}
	return baseU.ResolveReference(refU).String()
}

func resolveWebpackChunkURL(mainJSURL, chunkName, hash string) string {
	return resolveWebpackChunkURLWithSuffix(mainJSURL, chunkName, hash, "a.js")
}

func resolveWebpackChunkURLWithSuffix(mainJSURL, chunkName, hash, suffix string) string {
	if suffix == "" {
		suffix = "a.js"
	}
	u, err := url.Parse(mainJSURL)
	if err != nil {
		return "https://" + StaticAssetHost + StaticAssetPathPrefix + "client-web/" + chunkName + "." + hash + suffix
	}
	dir := path.Dir(u.Path)
	if dir == "." || dir == "/" {
		dir = StaticAssetPathPrefix + "client-web"
	}
	file := chunkName + "." + hash + suffix
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

// ---------- main.*.js & Lazy Chunk Operation Parsing (Webpack + Vite) ----------

const graphqlOperationMatchNames = `UserByScreenName|UserByRestId|UserTweets|UserTweetsAndReplies|UserMedia|UserHighlightsTweets|UserArticlesTweets|TweetDetail|Following|Bookmarks|BookmarkTimeline|BookmarksTimeline|Likes|CombinedLists|ListOwnerships|ListByRestId|ListLatestTweetsTimeline|ListMembers`

var (
	reExplicitBearerLit = regexp.MustCompile(`["'` + "`" + `]Bearer\s+([A-Za-z0-9%+/=._-]{16,256})["'` + "`" + `]`)
	reNamedBearerAssign = regexp.MustCompile(`(?i)\b(?:bearer[_]?token|webBearer|public[_]?bearer)\b\s*[:=]\s*["'` + "`" + `]([A-Za-z0-9%+/=._-]{16,256})["'` + "`" + `]`)
	reClassicWebBearer  = regexp.MustCompile(`["'` + "`" + `](A{16,}[A-Za-z0-9%+/=_-]{40,200})["'` + "`" + `]`)
	reValidQueryID      = regexp.MustCompile(`^[A-Za-z0-9_-]{6,64}$`)
	reValidFeatureKey   = regexp.MustCompile(`^[A-Za-z0-9_.-]{3,128}$`)
	reOpNameMatch       = regexp.MustCompile(`(?:["'` + "`" + `]?operationName["'` + "`" + `]?|["'` + "`" + `]?name["'` + "`" + `]?)\s*:\s*["'` + "`" + `](` + graphqlOperationMatchNames + `)["'` + "`" + `]`)
	reOpNameAssign      = regexp.MustCompile(`(?:\.\s*(?:operationName|name)|\b(?:operationName|name))\s*=\s*["'` + "`" + `](` + graphqlOperationMatchNames + `)["'` + "`" + `]`)
	reHarmonyExportCall = regexp.MustCompile(`\.d\(\s*[A-Za-z_$][A-Za-z0-9_$]*\s*,\s*(\{[^}]{10,600}\})\s*\)`)
	reHarmonyGetterProp = regexp.MustCompile(`(?:["']?([A-Za-z_$][A-Za-z0-9_$]*)["']?)\s*:\s*(?:function\s*\(\s*\)\s*\{\s*return\s*([^;}]+?)\s*;?\s*\}|\(\s*\)\s*=>\s*([^,}]+))`)
	reAssignQIDProp     = regexp.MustCompile(`(?:\.\s*(?:queryId|id|doc_id|query_id)|\b(?:queryId|id|doc_id|query_id))\s*=\s*(?:["'` + "`" + `]([A-Za-z0-9_-]{6,64})["'` + "`" + `]|([A-Za-z_$][A-Za-z0-9_$]*))`)
	reAssignOpTypeProp  = regexp.MustCompile(`(?:\.\s*(?:operationType|operationKind)|\b(?:operationType|operationKind))\s*=\s*(?:["'` + "`" + `]([A-Za-z]+)["'` + "`" + `]|([A-Za-z_$][A-Za-z0-9_$]*))`)
	reAssignMetaProp    = regexp.MustCompile(`(?:\.\s*(?:metadata|features|featureSwitches|fieldToggles)|\b(?:metadata|features|featureSwitches|fieldToggles))\s*=\s*`)
	reShallowQIDField   = regexp.MustCompile(`(?:["'` + "`" + `]?(?:queryId|id|doc_id|query_id)["'` + "`" + `]?)\s*:\s*["'` + "`" + `]([A-Za-z0-9_-]{6,64})["'` + "`" + `]`)
	reShallowOpType     = regexp.MustCompile(`(?:["'` + "`" + `]?(?:operationType|operationKind)["'` + "`" + `]?)\s*:\s*["'` + "`" + `]([A-Za-z]+)["'` + "`" + `]`)
	reShallowFeatArray  = regexp.MustCompile(`["'` + "`" + `]?featureSwitches["'` + "`" + `]?\s*:\s*(\[[^\]]*\])`)
	reShallowToggArray  = regexp.MustCompile(`["'` + "`" + `]?fieldToggles["'` + "`" + `]?\s*:\s*(\[[^\]]*\])`)
	reValueBoolDefault  = regexp.MustCompile(`(?:["']([A-Za-z0-9_.-]+)["']|\b([A-Za-z_][A-Za-z0-9_]*))\s*:\s*\{\s*["']?value["']?\s*:\s*(true|false|!0|!1)\b`)
	reFlatBoolDefault   = regexp.MustCompile(`(?:["']([A-Za-z0-9_.-]+)["']|\b([A-Za-z_][A-Za-z0-9_]*))\s*:\s*(true|false|!0|!1)\b`)
	reNullFeaturesErr   = regexp.MustCompile(`(?i)features cannot be null\s*:\s*([^\r\n"'}\]]+)`)
)

// jsIdent represents an unquoted JavaScript identifier encountered as a property value
// (e.g. `queryId: i` or `metadata: a`) so local module-scoped bindings can be resolved.
type jsIdent string

type rawOpCandidate struct {
	canonicalOp       string
	wireOpName        string
	isAlias           bool
	queryID           string
	featuresMap       map[string]bool
	featureSwitchKeys []string
	fieldTogglesMap   map[string]bool
	fieldToggleKeys   []string
}

func canonicalGraphQLOpName(rawName string) (canonical string, isAlias bool, ok bool) {
	switch rawName {
	case OpUserByScreenName,
		OpUserByRestId,
		OpUserTweets,
		OpUserTweetsAndReplies,
		OpUserMedia,
		OpUserHighlightsTweets,
		OpUserArticlesTweets,
		OpTweetDetail,
		OpFollowing,
		OpBookmarks,
		OpLikes,
		OpCombinedLists,
		OpListOwnerships,
		OpListByRestId,
		OpListLatestTweetsTimeline,
		OpListMembers:
		return rawName, false, true
	case "BookmarkTimeline", "BookmarksTimeline":
		return OpBookmarks, true, true
	default:
		return "", false, false
	}
}

// ExtractNullFeatureNames parses missing feature switch names from an X HTTP 400
// "The following features cannot be null: a, b, c" response body.
func ExtractNullFeatureNames(body []byte) []string {
	if len(body) == 0 {
		return nil
	}
	matches := reNullFeaturesErr.FindAllSubmatch(body, -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		for _, part := range strings.Split(string(m[1]), ",") {
			key := strings.TrimSpace(part)
			key = strings.Trim(key, `"'`+"`"+`.:;`)
			if key == "" || !reValidFeatureKey.MatchString(key) || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, key)
		}
	}
	return out
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

func shouldReplaceOpCandidate(prev rawOpCandidate, exists bool, next rawOpCandidate) bool {
	if !exists {
		return true
	}
	// Canonical operation names (e.g. "Bookmarks") strictly take precedence over aliases
	// (e.g. "BookmarkTimeline").
	if prev.isAlias && !next.isAlias {
		return true
	}
	if !prev.isAlias && next.isAlias {
		return false
	}
	return candidateScore(next.featuresMap, next.featureSwitchKeys) > candidateScore(prev.featuresMap, prev.featureSwitchKeys)
}

func extractOperationCandidates(js string) map[string]rawOpCandidate {
	candidates := make(map[string]rawOpCandidate, 7)
	putCand := func(cand rawOpCandidate) {
		if cand.canonicalOp == "" || !reValidQueryID.MatchString(cand.queryID) {
			return
		}
		prev, exists := candidates[cand.canonicalOp]
		if shouldReplaceOpCandidate(prev, exists, cand) {
			candidates[cand.canonicalOp] = cand
		}
	}

	// Pass 1: Object literals `{ queryId/id/doc_id, operationName/name, ... }` (including
	// Relay `{ params: {...} }`, hoisted local identifiers, spread/shorthand properties,
	// and shallow window fallback when enclosing AST exceeds object parser bounds).
	for _, loc := range reOpNameMatch.FindAllStringSubmatchIndex(js, -1) {
		rawOpName := js[loc[2]:loc[3]]
		var (
			cand  rawOpCandidate
			valid bool
		)
		if objSrc, ok := extractEnclosingJSObject(js, loc[0]); ok {
			if parsedObj, ok2 := parseJSObject(objSrc); ok2 {
				cand, valid = decodeOperationCandidateScoped(rawOpName, parsedObj, js, loc[0])
			}
		}
		if !valid {
			outerStart := findEnclosingBraceStart(js, loc[0])
			if outerStart > 0 {
				if outerSrc, ok2 := extractEnclosingJSObject(js, outerStart-1); ok2 {
					if outerObj, ok3 := parseJSObject(outerSrc); ok3 {
						cand, valid = decodeOperationCandidateScoped(rawOpName, outerObj, js, outerStart)
					}
				}
			}
		}
		if !valid {
			cand, valid = extractShallowWindowCandidate(js, loc[0], loc[1], rawOpName)
		}
		if valid {
			putCand(cand)
		}
	}

	// Pass 2: CommonJS / ESM property or variable assignments
	// (e.g. `t.queryId="..."; t.operationName="Bookmarks"; t.metadata={...}`).
	for _, loc := range reOpNameAssign.FindAllStringSubmatchIndex(js, -1) {
		rawOpName := js[loc[2]:loc[3]]
		if cand, ok := extractAssignedOperationCandidate(js, loc[0], rawOpName); ok {
			putCand(cand)
		}
	}

	// Pass 3: Webpack 5 Harmony ESM export descriptors
	// (e.g. `n.d(t,{metadata:()=>a,operationName:()=>r,operationType:()=>o,queryId:()=>i})`).
	for _, loc := range reHarmonyExportCall.FindAllStringSubmatchIndex(js, -1) {
		if len(loc) < 4 || loc[2] < 0 || loc[3] < 0 {
			continue
		}
		desc := js[loc[2]:loc[3]]
		if !strings.Contains(desc, "operationName") && !strings.Contains(desc, "name") {
			continue
		}
		if !strings.Contains(desc, "queryId") && !strings.Contains(desc, "id") && !strings.Contains(desc, "doc_id") {
			continue
		}
		if cand, ok := extractHarmonyExportCandidate(js, loc[0], desc); ok {
			putCand(cand)
		}
	}

	return candidates
}

func extractShallowWindowCandidate(js string, matchStart, matchEnd int, rawOpName string) (rawOpCandidate, bool) {
	canonical, isAlias, ok := canonicalGraphQLOpName(rawOpName)
	if !ok {
		return rawOpCandidate{}, false
	}
	modStart, modEnd := enclosingModuleBounds(js, matchStart)
	winStart := max(modStart, matchStart-600)
	winEnd := min(modEnd, matchEnd+2400)
	win := js[winStart:winEnd]

	if tm := reShallowOpType.FindStringSubmatch(win); len(tm) == 2 {
		if !strings.EqualFold(tm[1], "query") {
			return rawOpCandidate{}, false
		}
	}
	qm := reShallowQIDField.FindStringSubmatch(win)
	if len(qm) != 2 || !reValidQueryID.MatchString(qm[1]) {
		return rawOpCandidate{}, false
	}
	out := rawOpCandidate{
		canonicalOp: canonical,
		wireOpName:  rawOpName,
		isAlias:     isAlias,
		queryID:     qm[1],
	}
	if fm := reShallowFeatArray.FindStringSubmatch(win); len(fm) == 2 {
		p := &jsParser{s: fm[1]}
		if arr, ok := p.parseArray(); ok {
			if keys, ok2 := toStringSlice(arr); ok2 {
				out.featureSwitchKeys = append(out.featureSwitchKeys, keys...)
			}
		}
	}
	if tm := reShallowToggArray.FindStringSubmatch(win); len(tm) == 2 {
		p := &jsParser{s: tm[1]}
		if arr, ok := p.parseArray(); ok {
			if keys, ok2 := toStringSlice(arr); ok2 {
				out.fieldToggleKeys = append(out.fieldToggleKeys, keys...)
			}
		}
	}
	return out, true
}

func extractAssignedOperationCandidate(js string, anchorPos int, rawOpName string) (rawOpCandidate, bool) {
	canonical, isAlias, ok := canonicalGraphQLOpName(rawOpName)
	if !ok {
		return rawOpCandidate{}, false
	}
	modStart, modEnd := enclosingModuleBounds(js, anchorPos)
	winStart := max(modStart, anchorPos-2048)
	winEnd := min(modEnd, anchorPos+2048)
	win := js[winStart:winEnd]

	for _, tm := range reAssignOpTypeProp.FindAllStringSubmatch(win, -1) {
		opType := tm[1]
		if opType == "" && tm[2] != "" {
			if resolved, ok := resolveLocalJSIdent(js, anchorPos, tm[2]).(string); ok {
				opType = resolved
			}
		}
		if opType != "" && !strings.EqualFold(opType, "query") {
			return rawOpCandidate{}, false
		}
	}

	var qid string
	for _, qm := range reAssignQIDProp.FindAllStringSubmatch(win, -1) {
		candQID := qm[1]
		if candQID == "" && qm[2] != "" && qm[2] != "void" && qm[2] != "undefined" {
			if resolved, ok := resolveLocalJSIdent(js, anchorPos, qm[2]).(string); ok {
				candQID = resolved
			}
		}
		if reValidQueryID.MatchString(candQID) {
			qid = candQID
			break
		}
	}
	if qid == "" {
		return rawOpCandidate{}, false
	}

	out := rawOpCandidate{
		canonicalOp: canonical,
		wireOpName:  rawOpName,
		isAlias:     isAlias,
		queryID:     qid,
	}

	for _, loc := range reAssignMetaProp.FindAllStringIndex(win, -1) {
		propAssign := win[loc[0]:loc[1]]
		p := &jsParser{s: win, i: loc[1]}
		val := resolveMaybeIdent(p.parseValue(), js, anchorPos)
		switch {
		case strings.Contains(propAssign, "metadata"):
			if m, ok := val.(map[string]any); ok {
				applyCandidateSwitchSource(&out, m, js, anchorPos)
			}
		case strings.Contains(propAssign, "features"):
			if fMap, ok := toBoolMap(val); ok {
				out.featuresMap = mergeBoolMaps(out.featuresMap, fMap)
			}
		case strings.Contains(propAssign, "featureSwitches"):
			if fsMap, ok := toBoolMap(val); ok {
				out.featuresMap = mergeBoolMaps(out.featuresMap, fsMap)
			} else if fsList, ok := toStringSlice(val); ok {
				out.featureSwitchKeys = append(out.featureSwitchKeys, fsList...)
			}
		case strings.Contains(propAssign, "fieldToggles"):
			if ftMap, ok := toBoolMap(val); ok {
				out.fieldTogglesMap = mergeBoolMaps(out.fieldTogglesMap, ftMap)
			} else if ftList, ok := toStringSlice(val); ok {
				out.fieldToggleKeys = append(out.fieldToggleKeys, ftList...)
			}
		}
	}

	return out, true
}

func extractHarmonyExportCandidate(js string, anchorPos int, descriptor string) (rawOpCandidate, bool) {
	props := make(map[string]any)
	for _, m := range reHarmonyGetterProp.FindAllStringSubmatch(descriptor, -1) {
		key := m[1]
		expr := strings.TrimSpace(m[2])
		if expr == "" {
			expr = strings.TrimSpace(m[3])
		}
		expr = strings.TrimPrefix(expr, "(")
		expr = strings.TrimSuffix(expr, ")")
		p := &jsParser{s: expr}
		props[key] = resolveMaybeIdent(p.parseValue(), js, anchorPos)
	}

	rawOpName := firstStringField(props, "operationName", "name")
	if rawOpName == "" {
		return rawOpCandidate{}, false
	}
	return decodeOperationCandidateScoped(rawOpName, props, js, anchorPos)
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

		wireName := cand.wireOpName
		if wireName == "" {
			wireName = opName
		}

		ops[opName] = GraphQLOperationSpec{
			OperationName: wireName,
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
	return decodeOperationCandidateScoped(expectedOp, obj, "", 0)
}

func decodeOperationCandidateScoped(expectedOp string, obj map[string]any, fullJS string, anchorPos int) (rawOpCandidate, bool) {
	var out rawOpCandidate
	canonical, isAlias, ok := canonicalGraphQLOpName(expectedOp)
	if !ok {
		return out, false
	}

	// Unwrap Vite/Relay `{ params: { id, name, metadata } }` wrapper (inline or hoisted ident).
	if rawParams, hasParams := obj["params"]; hasParams {
		if params, ok := resolveMaybeIdent(rawParams, fullJS, anchorPos).(map[string]any); ok {
			obj = params
		}
	}

	opName := firstStringFieldScoped(obj, fullJS, anchorPos, "operationName", "name")
	if opName != expectedOp {
		return out, false
	}
	opType := strings.ToLower(firstStringFieldScoped(obj, fullJS, anchorPos, "operationType", "operationKind"))
	if opType != "" && opType != "query" {
		return out, false
	}

	qid := firstStringFieldScoped(obj, fullJS, anchorPos, "queryId", "id", "doc_id", "query_id")
	if !reValidQueryID.MatchString(qid) {
		return out, false
	}

	out.canonicalOp = canonical
	out.wireOpName = opName
	out.isAlias = isAlias
	out.queryID = qid

	applyCandidateSwitchSource(&out, obj, fullJS, anchorPos)
	if rawMeta, hasMeta := obj["metadata"]; hasMeta {
		if meta, ok := resolveMaybeIdent(rawMeta, fullJS, anchorPos).(map[string]any); ok {
			applyCandidateSwitchSource(&out, meta, fullJS, anchorPos)
		}
	}
	return out, true
}

func applyCandidateSwitchSource(out *rawOpCandidate, src map[string]any, fullJS string, anchorPos int) {
	if out == nil || src == nil {
		return
	}
	if rawFeat, ok := src["features"]; ok {
		resolved := resolveMaybeIdent(rawFeat, fullJS, anchorPos)
		if fMap, ok2 := toBoolMap(resolved); ok2 && len(fMap) > 0 {
			out.featuresMap = mergeBoolMaps(out.featuresMap, fMap)
		}
	}
	if rawFS, ok := src["featureSwitches"]; ok {
		resolved := resolveMaybeIdent(rawFS, fullJS, anchorPos)
		if fsMap, ok2 := toBoolMap(resolved); ok2 && len(fsMap) > 0 {
			out.featuresMap = mergeBoolMaps(out.featuresMap, fsMap)
		} else if fsList, ok2 := toStringSlice(resolved); ok2 && len(fsList) > 0 {
			out.featureSwitchKeys = append(out.featureSwitchKeys, fsList...)
		}
	}
	if rawFT, ok := src["fieldToggles"]; ok {
		resolved := resolveMaybeIdent(rawFT, fullJS, anchorPos)
		if ftMap, ok2 := toBoolMap(resolved); ok2 && len(ftMap) > 0 {
			out.fieldTogglesMap = mergeBoolMaps(out.fieldTogglesMap, ftMap)
		} else if ftList, ok2 := toStringSlice(resolved); ok2 && len(ftList) > 0 {
			out.fieldToggleKeys = append(out.fieldToggleKeys, ftList...)
		}
	}
}

func resolveMaybeIdent(v any, fullJS string, anchorPos int) any {
	id, ok := v.(jsIdent)
	if !ok || id == "" || fullJS == "" {
		return v
	}
	if resolved := resolveLocalJSIdent(fullJS, anchorPos, string(id)); resolved != nil {
		return resolved
	}
	return v
}

func firstStringField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func firstStringFieldScoped(m map[string]any, fullJS string, anchorPos int, keys ...string) string {
	for _, k := range keys {
		raw, exists := m[k]
		if !exists {
			continue
		}
		if s, ok := resolveMaybeIdent(raw, fullJS, anchorPos).(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func enclosingModuleBounds(js string, anchorPos int) (int, int) {
	if js == "" {
		return 0, 0
	}
	if anchorPos < 0 {
		anchorPos = 0
	}
	if anchorPos > len(js) {
		anchorPos = len(js)
	}
	// Walk outward up to 4 enclosing braces to find an enclosing webpack/AMD module
	// function body `{ ... }` (`function(...){` or `(...)=>{`) so minified single-letter
	// identifiers never resolve from a neighboring module in the same chunk.
	cur := anchorPos
	for depth := 0; depth < 4 && cur > 0; depth++ {
		openPos := findEnclosingBraceStartBounded(js, cur, 32768)
		if openPos < 0 {
			break
		}
		if isFunctionBodyOpenBrace(js, openPos) {
			closePos := findMatchingCloseBrace(js, openPos, openPos+131072)
			if closePos > openPos {
				return openPos + 1, closePos
			}
			return openPos + 1, min(len(js), openPos+32768)
		}
		cur = openPos
	}
	return max(0, anchorPos-16384), min(len(js), anchorPos+16384)
}

func isFunctionBodyOpenBrace(js string, bracePos int) bool {
	i := bracePos - 1
	for i >= 0 && (js[i] == ' ' || js[i] == '\t' || js[i] == '\r' || js[i] == '\n') {
		i--
	}
	if i < 0 {
		return false
	}
	if js[i] == ')' {
		return true
	}
	if i >= 1 && js[i-1] == '=' && js[i] == '>' {
		return true
	}
	return false
}

func resolveLocalJSIdent(js string, anchorPos int, ident string) any {
	if js == "" || ident == "" || ident == "null" || ident == "undefined" || ident == "void" {
		return nil
	}
	modStart, modEnd := enclosingModuleBounds(js, anchorPos)
	if modStart >= modEnd {
		return nil
	}
	scope := js[modStart:modEnd]
	relAnchor := anchorPos - modStart

	// Scan for `<boundary><ident>\s*=\s*` (not `==` or `=>`) inside the enclosing module scope,
	// preferring the closest non-void binding before anchorPos, then closest after anchorPos.
	var bestVal any
	bestBeforeDist := int(1 << 30)
	bestAfterDist := int(1 << 30)
	foundBefore := false

	searchFrom := 0
	for searchFrom < len(scope) {
		idx := strings.Index(scope[searchFrom:], ident)
		if idx < 0 {
			break
		}
		pos := searchFrom + idx
		searchFrom = pos + len(ident)

		if pos > 0 && isJSIdentChar(scope[pos-1]) {
			continue
		}
		if pos > 0 && scope[pos-1] == '.' {
			continue
		}
		afterIdent := pos + len(ident)
		if afterIdent < len(scope) && isJSIdentChar(scope[afterIdent]) {
			continue
		}
		eqPos := afterIdent
		for eqPos < len(scope) && (scope[eqPos] == ' ' || scope[eqPos] == '\t' || scope[eqPos] == '\r' || scope[eqPos] == '\n') {
			eqPos++
		}
		if eqPos >= len(scope) || scope[eqPos] != '=' {
			continue
		}
		if eqPos+1 < len(scope) && (scope[eqPos+1] == '=' || scope[eqPos+1] == '>') {
			continue
		}
		if eqPos > 0 && (scope[eqPos-1] == '!' || scope[eqPos-1] == '<' || scope[eqPos-1] == '>') {
			continue
		}

		p := &jsParser{s: scope, i: eqPos + 1}
		val := p.parseValue()
		if val == nil {
			continue
		}
		if _, isIdent := val.(jsIdent); isIdent {
			continue
		}

		if pos <= relAnchor {
			dist := relAnchor - pos
			if !foundBefore || dist < bestBeforeDist {
				foundBefore = true
				bestBeforeDist = dist
				bestVal = val
			}
		} else if !foundBefore {
			dist := pos - relAnchor
			if dist < bestAfterDist {
				bestAfterDist = dist
				bestVal = val
			}
		}
	}

	return bestVal
}

func isJSIdentChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '$'
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
	return findEnclosingBraceStartBounded(src, pos, 32768)
}

func findEnclosingBraceStartBounded(src string, pos, maxBackScan int) int {
	if pos > len(src) {
		pos = len(src)
	}
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

func findMatchingCloseBrace(src string, openBracePos, endLimit int) int {
	if openBracePos < 0 || openBracePos >= len(src) || src[openBracePos] != '{' {
		return -1
	}
	if endLimit > len(src) {
		endLimit = len(src)
	}
	depth := 0
	inStr := byte(0)
	for i := openBracePos; i < endLimit; i++ {
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
				return i
			}
		}
	}
	return -1
}

func extractEnclosingJSObject(src string, pos int) (string, bool) {
	start := findEnclosingBraceStart(src, pos)
	if start < 0 {
		return "", false
	}
	const maxObjLen = 131072
	endPos := findMatchingCloseBrace(src, start, start+maxObjLen)
	if endPos < 0 {
		return "", false
	}
	return src[start : endPos+1], true
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

		// Handle object spread `{ ...base, queryId: "..." }`.
		if strings.HasPrefix(p.s[p.i:], "...") {
			p.i += 3
			p.skipUnknownExpr()
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

		// Handle computed property keys `{ [expr]: val }`.
		if p.s[p.i] == '[' {
			p.skipBalancedBracket('[', ']')
			p.skipWS()
			if p.i < len(p.s) && p.s[p.i] == ':' {
				p.i++
				_ = p.parseValue()
			} else if p.i < len(p.s) && p.s[p.i] == '(' {
				p.skipMethodTail()
			}
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

		key, ok := p.parseKey()
		if !ok {
			return nil, false
		}
		p.skipWS()
		if p.i >= len(p.s) {
			return nil, false
		}

		// Handle `async method(){}` or `get prop(){}` / `set prop(v){}` modifiers.
		if (key == "async" || key == "get" || key == "set") && p.s[p.i] != ':' && p.s[p.i] != ',' && p.s[p.i] != '}' && p.s[p.i] != '(' {
			if p.s[p.i] == '[' {
				p.skipBalancedBracket('[', ']')
			} else {
				_, _ = p.parseKey()
			}
			p.skipWS()
		}

		if p.i < len(p.s) && p.s[p.i] == '(' {
			// ES6 method shorthand `foo() { ... }`.
			p.skipMethodTail()
		} else if p.i < len(p.s) && (p.s[p.i] == ',' || p.s[p.i] == '}') {
			// ES6 property shorthand `{ queryId, operationName: "Bookmarks", metadata }`.
			obj[key] = jsIdent(key)
		} else if p.i < len(p.s) && p.s[p.i] == ':' {
			p.i++
			val := p.parseValue()
			obj[key] = val
		} else {
			return nil, false
		}

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

func (p *jsParser) skipBalancedBracket(openCh, closeCh byte) {
	if p.i >= len(p.s) || p.s[p.i] != openCh {
		return
	}
	depth := 0
	for p.i < len(p.s) {
		c := p.s[p.i]
		if c == '"' || c == '\'' || c == '`' {
			_, _ = p.parseString()
			continue
		}
		if c == openCh {
			depth++
		} else if c == closeCh {
			depth--
			if depth == 0 {
				p.i++
				return
			}
		}
		p.i++
	}
}

func (p *jsParser) skipMethodTail() {
	p.skipWS()
	if p.i < len(p.s) && p.s[p.i] == '(' {
		p.skipBalancedBracket('(', ')')
	}
	p.skipWS()
	if p.i < len(p.s) && p.s[p.i] == '{' {
		p.skipBalancedBracket('{', '}')
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
		if strings.HasPrefix(p.s[p.i:], "...") {
			p.i += 3
			p.skipUnknownExpr()
		} else {
			arr = append(arr, p.parseValue())
		}
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
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_' || c == '$' || c == '-' {
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

	// Capture simple unquoted JS identifier references (e.g. `queryId: i`, `metadata: a`)
	// when immediately followed by an object/array delimiter or end-of-expression.
	if c := p.s[p.i]; (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' || c == '$' {
		start := p.i
		for p.i < len(p.s) && isJSIdentChar(p.s[p.i]) {
			p.i++
		}
		ident := p.s[start:p.i]
		save := p.i
		p.skipWS()
		if p.i >= len(p.s) || p.s[p.i] == ',' || p.s[p.i] == '}' || p.s[p.i] == ']' || p.s[p.i] == ';' {
			if ident == "null" || ident == "undefined" {
				return nil
			}
			return jsIdent(ident)
		}
		p.i = save
	}

	p.skipUnknownExpr()
	return nil
}

func (p *jsParser) isTokenEnd(idx int) bool {
	if idx >= len(p.s) {
		return true
	}
	return !isJSIdentChar(p.s[idx])
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
