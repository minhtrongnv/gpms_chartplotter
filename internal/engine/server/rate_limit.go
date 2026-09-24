package server

import (
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type rateLimitClass uint8

const (
	rateLimitNone rateLimitClass = iota
	rateLimitMutation
	rateLimitExpensive
	rateLimitCompute
)

type rateLimitRule struct {
	tokensPerSecond float64
	burst           float64
}

type rateLimitKey struct {
	client string
	class  rateLimitClass
}

type rateLimitBucket struct {
	tokens   float64
	updated  time.Time
	lastSeen time.Time
}

type rateLimitConfig struct {
	rules      [4]rateLimitRule
	idleTTL    time.Duration
	maxEntries int
	sweepEvery uint64
}

type requestRateLimiter struct {
	mu      sync.Mutex
	entries map[rateLimitKey]rateLimitBucket
	config  rateLimitConfig
	ops     uint64
}

func defaultRateLimitConfig() rateLimitConfig {
	var rules [4]rateLimitRule

	rules[rateLimitMutation] = rateLimitRule{
		tokensPerSecond: 60.0 / 60.0,
		burst:           20,
	}

	rules[rateLimitExpensive] = rateLimitRule{
		tokensPerSecond: 12.0 / 60.0,
		burst:           6,
	}

	rules[rateLimitCompute] = rateLimitRule{
		tokensPerSecond: 120.0 / 60.0,
		burst:           20,
	}

	return rateLimitConfig{
		rules:      rules,
		idleTTL:    10 * time.Minute,
		maxEntries: 4096,
		sweepEvery: 256,
	}
}

func newRequestRateLimiter() *requestRateLimiter {
	return newRequestRateLimiterWithConfig(defaultRateLimitConfig())
}

func newRequestRateLimiterWithConfig(config rateLimitConfig) *requestRateLimiter {
	if config.idleTTL <= 0 {
		config.idleTTL = 10 * time.Minute
	}
	if config.maxEntries <= 0 {
		config.maxEntries = 4096
	}
	if config.sweepEvery == 0 {
		config.sweepEvery = 256
	}

	return &requestRateLimiter{
		entries: make(map[rateLimitKey]rateLimitBucket),
		config:  config,
	}
}

// allow consumes one token for client+class and returns Retry-After seconds
// when the request must be rejected.
//
// Memory is O(active clients × used classes), hard-capped by maxEntries.
// Cleanup is amortized; there is no background goroutine to manage.
func (l *requestRateLimiter) allow(
	client string,
	class rateLimitClass,
	now time.Time,
) (bool, int) {
	if l == nil || class == rateLimitNone {
		return true, 0
	}

	rule := l.config.rules[class]
	if rule.tokensPerSecond <= 0 || rule.burst < 1 {
		return true, 0
	}

	if client == "" {
		client = "unknown"
	}

	key := rateLimitKey{client: client, class: class}

	l.mu.Lock()
	defer l.mu.Unlock()

	l.ops++
	if l.ops%l.config.sweepEvery == 0 {
		l.sweepLocked(now)
	}

	bucket, ok := l.entries[key]
	if !ok {
		l.makeRoomLocked(now)
		bucket = rateLimitBucket{
			tokens:   rule.burst,
			updated:  now,
			lastSeen: now,
		}
	}

	if elapsed := now.Sub(bucket.updated); elapsed > 0 {
		bucket.tokens += elapsed.Seconds() * rule.tokensPerSecond
		if bucket.tokens > rule.burst {
			bucket.tokens = rule.burst
		}
		bucket.updated = now
	}

	bucket.lastSeen = now

	if bucket.tokens >= 1 {
		bucket.tokens--
		l.entries[key] = bucket
		return true, 0
	}

	l.entries[key] = bucket

	retryAfter := int(math.Ceil((1 - bucket.tokens) / rule.tokensPerSecond))
	if retryAfter < 1 {
		retryAfter = 1
	}
	return false, retryAfter
}

func (l *requestRateLimiter) sweepLocked(now time.Time) {
	for key, bucket := range l.entries {
		if now.Sub(bucket.lastSeen) > l.config.idleTTL {
			delete(l.entries, key)
		}
	}
}

func (l *requestRateLimiter) makeRoomLocked(now time.Time) {
	if len(l.entries) < l.config.maxEntries {
		return
	}

	l.sweepLocked(now)
	if len(l.entries) < l.config.maxEntries {
		return
	}

	var (
		oldestKey  rateLimitKey
		oldestTime time.Time
		haveOldest bool
	)

	for key, bucket := range l.entries {
		if !haveOldest || bucket.lastSeen.Before(oldestTime) {
			oldestKey = key
			oldestTime = bucket.lastSeen
			haveOldest = true
		}
	}

	if haveOldest {
		delete(l.entries, oldestKey)
	}
}

// classifyRateLimitedRequest intentionally leaves read-heavy tiles/static/SSE
// outside the limiter. Only selected state-changing or expensive API operations
// get a bucket.
func classifyRateLimitedRequest(r *http.Request) rateLimitClass {
	if r == nil {
		return rateLimitNone
	}

	p := r.URL.Path

	switch {
	case r.Method == http.MethodPost &&
		(p == "/api/import" ||
			p == "/api/import/packs" ||
			p == "/api/plugins/install" ||
			p == "/api/debug/partition"):
		return rateLimitExpensive

	case r.Method == http.MethodPut &&
		strings.HasPrefix(p, "/api/cell/"):
		return rateLimitExpensive

	// This GET may actually download from the provider and populate the cache.
	case r.Method == http.MethodGet &&
		strings.HasPrefix(p, "/api/cell/") &&
		r.URL.Query().Get("url") != "":
		return rateLimitExpensive

	case r.Method == http.MethodPost && p == "/api/style-diff":
		return rateLimitCompute

	case r.Method == http.MethodPost &&
		(p == "/api/share" ||
			p == "/api/settings" ||
			p == "/api/connections" ||
			p == "/api/set/enable" ||
			p == "/api/set/disable"):
		return rateLimitMutation

	case r.Method == http.MethodDelete &&
		(p == "/api/set" || p == "/api/district"):
		return rateLimitMutation

	case strings.HasPrefix(p, "/api/connections/") &&
		isRateLimitedWriteMethod(r.Method):
		return rateLimitMutation

	case strings.HasPrefix(p, "/api/plugins/") &&
		p != "/api/plugins/install" &&
		isRateLimitedWriteMethod(r.Method):
		return rateLimitMutation
	}

	// /api/proxy is intentionally excluded: the browser's random-access ZIP
	// reader legitimately emits many Range GETs. Protecting that path should use
	// a separate outbound concurrency/bandwidth cap rather than request count.
	return rateLimitNone
}

func isRateLimitedWriteMethod(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	default:
		return false
	}
}

// SetClientIPResolver installs the trusted-proxy-aware resolver used by the
// limiter. Call it during setup, before ServeHTTP is used concurrently.
// Passing nil restores direct RemoteAddr-only behavior.
func (s *Server) SetClientIPResolver(resolver *ClientIPResolver) {
	if resolver == nil {
		resolver = &ClientIPResolver{}
	}
	s.clientIPs = resolver
}

func (s *Server) allowRateLimitedRequest(
	w http.ResponseWriter,
	r *http.Request,
) bool {
	class := classifyRateLimitedRequest(r)
	if class == rateLimitNone || s.rateLimiter == nil {
		return true
	}

	client := ""
	if s.clientIPs != nil {
		client = s.clientIPs.ClientIP(r)
	}

	allowed, retryAfter := s.rateLimiter.allow(client, class, time.Now())
	if allowed {
		return true
	}

	w.Header().Set("Retry-After", strconv.Itoa(retryAfter))
	w.Header().Set("Cache-Control", "no-store")
	apiErr(w, http.StatusTooManyRequests, "rate limit exceeded")
	return false
}
