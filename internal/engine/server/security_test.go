package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHostIsLocal(t *testing.T) {
	cases := []struct {
		host string
		want bool
	}{
		{"127.0.0.1", true},
		{"127.0.0.1:8080", true},
		{"127.0.0.2:8080", true},
		{"localhost", true},
		{"LOCALHOST:8080", true},
		{"localhost.", true},
		{"[::1]", true},
		{"[::1]:8080", true},
		{"::1", true},
		{"localhost.evil.example", false},
		{"127.0.0.1.evil.example", false},
		{"evil.example", false},
		{"", false},
	}

	for _, tc := range cases {
		t.Run(tc.host, func(t *testing.T) {
			if got := hostIsLocal(tc.host); got != tc.want {
				t.Fatalf(
					"hostIsLocal(%q) = %v, want %v",
					tc.host,
					got,
					tc.want,
				)
			}
		})
	}
}

func TestLoopbackHostGuardRejectsPrefixSpoof(t *testing.T) {
	s := New("", t.TempDir(), t.TempDir(), false, "")
	defer s.Close()

	r := httptest.NewRequest(
		http.MethodGet,
		"http://127.0.0.1:8080/api/health",
		nil,
	)
	r.Host = "localhost.evil.example"

	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf(
			"prefix-spoofed Host status = %d, want 403",
			w.Code,
		)
	}
}

func TestPeerIsLoopback(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"127.0.0.1:50000", true},
		{"[::1]:50000", true},
		{"127.0.0.1", true},
		{"192.168.1.10:50000", false},
		{"203.0.113.10:50000", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := peerIsLoopback(tc.addr); got != tc.want {
			t.Errorf("peerIsLoopback(%q) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

func TestLoopbackReverseProxyMayUsePublicHost(t *testing.T) {
	s := New("", t.TempDir(), t.TempDir(), false, "")
	defer s.Close()

	r := httptest.NewRequest(http.MethodGet, "http://chartplotter.trongnguyenlabs.cloud/api/health", nil)
	r.Host = "chartplotter.trongnguyenlabs.cloud"
	r.RemoteAddr = "127.0.0.1:54321"

	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("loopback reverse-proxy status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestPublicHostStillRejectedFromNonLoopbackPeer(t *testing.T) {
	s := New("", t.TempDir(), t.TempDir(), false, "")
	defer s.Close()

	r := httptest.NewRequest(http.MethodGet, "http://chartplotter.trongnguyenlabs.cloud/api/health", nil)
	r.Host = "chartplotter.trongnguyenlabs.cloud"
	r.RemoteAddr = "192.168.1.50:54321"

	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Fatalf("non-loopback public-host status = %d, want 403", w.Code)
	}
}

func TestSSEHeadersDoNotAllowCrossOrigin(t *testing.T) {
	w := httptest.NewRecorder()

	if _, ok := sseStart(w); !ok {
		t.Fatal("expected httptest recorder to support streaming")
	}

	if got := w.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Fatalf(
			"Access-Control-Allow-Origin = %q, want empty for live SSE",
			got,
		)
	}

	if got := w.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf(
			"Content-Type = %q, want text/event-stream",
			got,
		)
	}
}

func TestSafeAuxStoredName(t *testing.T) {
	cases := []struct {
		name string
		want bool
	}{
		{"PIC01.png", true},
		{"README.txt", true},
		{"../secret", false},
		{"subdir/file.png", false},
		{"subdir\\file.png", false},
		{"..", false},
		{"", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := safeAuxStoredName(tc.name); got != tc.want {
				t.Fatalf(
					"safeAuxStoredName(%q) = %v, want %v",
					tc.name,
					got,
					tc.want,
				)
			}
		})
	}
}

func TestAllowedChartURL(t *testing.T) {
	cases := []struct {
		url  string
		want bool
	}{
		{"https://charts.noaa.gov/ENCs/US5MD1MC.zip", true},
		{"https://www.charts.noaa.gov/ENCs/All_ENCs.zip", true}, // subdomain
		{"http://ienccloud.us/foo.zip", true},
		{"https://charts.noaa.gov.evil.com/x.zip", false}, // suffix spoof
		{"https://evil-charts.noaa.gov/x.zip", false},     // prefix spoof
		{"https://evil.com/x.zip", false},
		{"http://169.254.169.254/latest/meta-data/", false}, // cloud metadata SSRF
		{"http://127.0.0.1:8080/api/settings", false},       // loopback SSRF
		{"file:///etc/passwd", false},                       // non-http scheme
		{"ftp://charts.noaa.gov/x", false},
		{"", false},
		{"not a url", false},
	}
	for _, c := range cases {
		if got := allowedChartURL(c.url); got != c.want {
			t.Errorf("allowedChartURL(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}

func TestCrossSiteWrite(t *testing.T) {
	mk := func(method string, hdr map[string]string) *http.Request {
		r := httptest.NewRequest(method, "http://127.0.0.1:8080/api/settings", nil)
		r.Host = "127.0.0.1:8080"
		for k, v := range hdr {
			r.Header.Set(k, v)
		}
		return r
	}
	cases := []struct {
		name string
		req  *http.Request
		want bool // want blocked
	}{
		{"GET always allowed", mk("GET", map[string]string{"Sec-Fetch-Site": "cross-site"}), false},
		{"same-origin write allowed", mk("POST", map[string]string{"Sec-Fetch-Site": "same-origin"}), false},
		{"cross-site write blocked", mk("POST", map[string]string{"Sec-Fetch-Site": "cross-site"}), true},
		{"same-site write blocked", mk("POST", map[string]string{"Sec-Fetch-Site": "same-site"}), true},
		{"no headers (non-browser) allowed", mk("POST", nil), false},
		{"matching Origin allowed", mk("POST", map[string]string{"Origin": "http://127.0.0.1:8080"}), false},
		{"foreign Origin blocked", mk("POST", map[string]string{"Origin": "https://evil.com"}), true},
	}
	for _, c := range cases {
		if got := crossSiteWrite(c.req); got != c.want {
			t.Errorf("%s: crossSiteWrite = %v, want %v", c.name, got, c.want)
		}
	}
}

// A cross-site POST to a state-changing endpoint must be rejected before any
// handler runs, and security headers must be present on every response.
func TestHandlerBlocksCrossSiteAndSetsHeaders(t *testing.T) {
	s := New("", t.TempDir(), t.TempDir(), false, "")
	defer s.Close()

	r := httptest.NewRequest("POST", "http://127.0.0.1:8080/api/settings", nil)
	r.Host = "127.0.0.1:8080"
	r.Header.Set("Origin", "https://evil.com")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)

	if w.Code != http.StatusForbidden {
		t.Errorf("cross-site POST: status = %d, want 403", w.Code)
	}
	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q, want nosniff", got)
	}
	if got := w.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("X-Frame-Options = %q, want DENY", got)
	}
	if got := w.Header().Get("Content-Security-Policy"); got != contentSecurityPolicy {
		t.Errorf("Content-Security-Policy = %q, want %q", got, contentSecurityPolicy)
	}
	if got := w.Header().Get("Referrer-Policy"); got != "no-referrer" {
		t.Errorf("Referrer-Policy = %q, want no-referrer", got)
	}
	if got := w.Header().Get("X-Permitted-Cross-Domain-Policies"); got != "none" {
		t.Errorf("X-Permitted-Cross-Domain-Policies = %q, want none", got)
	}
}

// The proxy must refuse a non-provider URL (SSRF).
func TestProxyRejectsNonProviderURL(t *testing.T) {
	s := New("", t.TempDir(), t.TempDir(), false, "")
	defer s.Close()

	r := httptest.NewRequest("GET", "http://127.0.0.1:8080/api/proxy?url=http://169.254.169.254/", nil)
	r.Host = "127.0.0.1:8080"
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("SSRF proxy: status = %d, want 400", w.Code)
	}
}
