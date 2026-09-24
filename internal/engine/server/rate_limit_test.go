package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func testRateLimitConfig(
	class rateLimitClass,
	perSecond float64,
	burst float64,
) rateLimitConfig {
	var rules [4]rateLimitRule
	rules[class] = rateLimitRule{
		tokensPerSecond: perSecond,
		burst:           burst,
	}

	return rateLimitConfig{
		rules:      rules,
		idleTTL:    time.Minute,
		maxEntries: 32,
		sweepEvery: 4,
	}
}

func TestClassifyRateLimitedRequest(t *testing.T) {
	tests := []struct {
		method string
		target string
		want   rateLimitClass
	}{
		{http.MethodPost, "/api/import", rateLimitExpensive},
		{http.MethodPost, "/api/import/packs", rateLimitExpensive},
		{http.MethodPost, "/api/plugins/install", rateLimitExpensive},
		{http.MethodPut, "/api/cell/US5MD1MC", rateLimitExpensive},
		{http.MethodGet, "/api/cell/US5MD1MC?url=https://charts.noaa.gov/example.zip", rateLimitExpensive},
		{http.MethodGet, "/api/cell/US5MD1MC", rateLimitNone},
		{http.MethodPost, "/api/debug/partition", rateLimitExpensive},
		{http.MethodGet, "/api/debug/partition", rateLimitNone},
		{http.MethodPost, "/api/share", rateLimitMutation},
		{http.MethodPost, "/api/settings", rateLimitMutation},
		{http.MethodPost, "/api/connections", rateLimitMutation},
		{http.MethodPut, "/api/connections/abc", rateLimitMutation},
		{http.MethodDelete, "/api/connections/abc", rateLimitMutation},
		{http.MethodPost, "/api/plugins/abc/config", rateLimitMutation},
		{http.MethodDelete, "/api/plugins/abc", rateLimitMutation},
		{http.MethodPost, "/api/set/enable?set=noaa", rateLimitMutation},
		{http.MethodDelete, "/api/set?set=noaa", rateLimitMutation},
		{http.MethodDelete, "/api/district?provider=noaa&district=d5", rateLimitMutation},
		{http.MethodPost, "/api/style-diff", rateLimitCompute},
		{http.MethodGet, "/api/import/status?job=x", rateLimitNone},
		{http.MethodGet, "/api/import/events?job=x", rateLimitNone},
		{http.MethodGet, "/api/vessel/stream", rateLimitNone},
		{http.MethodGet, "/api/proxy?url=https://charts.noaa.gov/example.zip", rateLimitNone},
	}

	for _, tt := range tests {
		t.Run(tt.method+" "+tt.target, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.target, nil)
			if got := classifyRateLimitedRequest(req); got != tt.want {
				t.Fatalf("got class %d, want %d", got, tt.want)
			}
		})
	}
}

func TestRateLimiterBurstAndRefill(t *testing.T) {
	limiter := newRequestRateLimiterWithConfig(
		testRateLimitConfig(rateLimitExpensive, 1, 2),
	)

	now := time.Unix(1000, 0)

	if ok, _ := limiter.allow("192.0.2.10", rateLimitExpensive, now); !ok {
		t.Fatal("first request should pass")
	}
	if ok, _ := limiter.allow("192.0.2.10", rateLimitExpensive, now); !ok {
		t.Fatal("second burst request should pass")
	}
	if ok, retry := limiter.allow("192.0.2.10", rateLimitExpensive, now); ok || retry != 1 {
		t.Fatalf("third request should be limited with Retry-After 1, got ok=%v retry=%d", ok, retry)
	}
	if ok, _ := limiter.allow("192.0.2.10", rateLimitExpensive, now.Add(time.Second)); !ok {
		t.Fatal("one token should refill after one second")
	}
}

func TestRateLimiterSeparatesClients(t *testing.T) {
	limiter := newRequestRateLimiterWithConfig(
		testRateLimitConfig(rateLimitMutation, 1, 1),
	)
	now := time.Unix(2000, 0)

	if ok, _ := limiter.allow("192.0.2.1", rateLimitMutation, now); !ok {
		t.Fatal("client one first request should pass")
	}
	if ok, _ := limiter.allow("192.0.2.1", rateLimitMutation, now); ok {
		t.Fatal("client one second request should be limited")
	}
	if ok, _ := limiter.allow("192.0.2.2", rateLimitMutation, now); !ok {
		t.Fatal("client two must have an independent bucket")
	}
}

func TestRateLimiterHardMemoryBound(t *testing.T) {
	config := testRateLimitConfig(rateLimitMutation, 1, 1)
	config.maxEntries = 2
	config.sweepEvery = 1000

	limiter := newRequestRateLimiterWithConfig(config)
	now := time.Unix(3000, 0)

	limiter.allow("192.0.2.1", rateLimitMutation, now)
	limiter.allow("192.0.2.2", rateLimitMutation, now.Add(time.Second))
	limiter.allow("192.0.2.3", rateLimitMutation, now.Add(2*time.Second))

	if got := len(limiter.entries); got > config.maxEntries {
		t.Fatalf("entries=%d exceeds max=%d", got, config.maxEntries)
	}
}

func TestRateLimitUsesTrustedProxyClientIP(t *testing.T) {
	resolver, err := NewClientIPResolver("127.0.0.1/32", true)
	if err != nil {
		t.Fatal(err)
	}

	s := &Server{
		clientIPs: resolver,
		rateLimiter: newRequestRateLimiterWithConfig(
			testRateLimitConfig(rateLimitMutation, 0.01, 1),
		),
	}

	do := func(client string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/share", nil)
		req.RemoteAddr = "127.0.0.1:50000"
		req.Header.Set("CF-Connecting-IP", client)

		rec := httptest.NewRecorder()
		if s.allowRateLimitedRequest(rec, req) {
			return http.StatusOK
		}
		return rec.Code
	}

	if got := do("203.0.113.10"); got != http.StatusOK {
		t.Fatalf("first client: got %d", got)
	}
	if got := do("203.0.113.10"); got != http.StatusTooManyRequests {
		t.Fatalf("same client should be limited: got %d", got)
	}
	if got := do("203.0.113.11"); got != http.StatusOK {
		t.Fatalf("different proxied client should have separate bucket: got %d", got)
	}
}

func TestRateLimitIgnoresSpoofedProxyHeader(t *testing.T) {
	resolver, err := NewClientIPResolver("127.0.0.1/32", true)
	if err != nil {
		t.Fatal(err)
	}

	s := &Server{
		clientIPs: resolver,
		rateLimiter: newRequestRateLimiterWithConfig(
			testRateLimitConfig(rateLimitMutation, 0.01, 1),
		),
	}

	do := func(fakeClient string) int {
		req := httptest.NewRequest(http.MethodPost, "/api/share", nil)
		req.RemoteAddr = "192.0.2.55:50000"
		req.Header.Set("CF-Connecting-IP", fakeClient)

		rec := httptest.NewRecorder()
		if s.allowRateLimitedRequest(rec, req) {
			return http.StatusOK
		}
		return rec.Code
	}

	if got := do("203.0.113.1"); got != http.StatusOK {
		t.Fatalf("first request: got %d", got)
	}
	if got := do("203.0.113.2"); got != http.StatusTooManyRequests {
		t.Fatalf("spoofed header must not evade bucket: got %d", got)
	}
}

func TestRateLimitResponseHasRetryAfter(t *testing.T) {
	s := &Server{
		clientIPs: &ClientIPResolver{},
		rateLimiter: newRequestRateLimiterWithConfig(
			testRateLimitConfig(rateLimitExpensive, 1, 1),
		),
	}

	req := httptest.NewRequest(http.MethodPost, "/api/import", nil)
	req.RemoteAddr = "192.0.2.100:50000"

	first := httptest.NewRecorder()
	if !s.allowRateLimitedRequest(first, req) {
		t.Fatal("first request should pass")
	}

	second := httptest.NewRecorder()
	if s.allowRateLimitedRequest(second, req) {
		t.Fatal("second request should be limited")
	}
	if second.Code != http.StatusTooManyRequests {
		t.Fatalf("status=%d, want 429", second.Code)
	}
	if second.Header().Get("Retry-After") == "" {
		t.Fatal("missing Retry-After")
	}
}
