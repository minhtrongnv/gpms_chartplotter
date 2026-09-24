package server

import (
	"net/http/httptest"
	"testing"
)

func TestClientIPDirectRequest(
	t *testing.T,
) {
	resolver, err := NewClientIPResolver(
		"",
		false,
	)

	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(
		"GET",
		"/",
		nil,
	)

	req.RemoteAddr = "192.168.10.25:54321"

	req.Header.Set(
		"CF-Connecting-IP",
		"8.8.8.8",
	)

	req.Header.Set(
		"X-Forwarded-For",
		"1.2.3.4",
	)

	got := resolver.ClientIP(req)

	if got != "192.168.10.25" {
		t.Fatalf(
			"expected direct client IP, got %q",
			got,
		)
	}
}

func TestClientIPCloudflare(
	t *testing.T,
) {
	resolver, err := NewClientIPResolver(
		"127.0.0.1/32,::1/128",
		true,
	)

	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(
		"GET",
		"/",
		nil,
	)

	req.RemoteAddr = "127.0.0.1:50000"

	req.Header.Set(
		"CF-Connecting-IP",
		"203.0.113.50",
	)

	got := resolver.ClientIP(req)

	if got != "203.0.113.50" {
		t.Fatalf(
			"expected Cloudflare client IP, got %q",
			got,
		)
	}
}

func TestClientIPIgnoresSpoofedCloudflareHeader(
	t *testing.T,
) {
	resolver, err := NewClientIPResolver(
		"127.0.0.1/32",
		true,
	)

	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(
		"GET",
		"/",
		nil,
	)

	req.RemoteAddr = "192.168.10.25:50000"

	req.Header.Set(
		"CF-Connecting-IP",
		"8.8.8.8",
	)

	got := resolver.ClientIP(req)

	if got != "192.168.10.25" {
		t.Fatalf(
			"expected RemoteAddr, got %q",
			got,
		)
	}
}

func TestClientIPXForwardedFor(
	t *testing.T,
) {
	resolver, err := NewClientIPResolver(
		"127.0.0.1/32,10.0.0.0/8",
		false,
	)

	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(
		"GET",
		"/",
		nil,
	)

	req.RemoteAddr = "127.0.0.1:50000"

	req.Header.Set(
		"X-Forwarded-For",
		"203.0.113.20, 10.1.1.20",
	)

	got := resolver.ClientIP(req)

	if got != "203.0.113.20" {
		t.Fatalf(
			"expected original client IP, got %q",
			got,
		)
	}
}

func TestClientIPRejectsMalformedXForwardedFor(
	t *testing.T,
) {
	resolver, err := NewClientIPResolver(
		"127.0.0.1/32",
		false,
	)

	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(
		"GET",
		"/",
		nil,
	)

	req.RemoteAddr = "127.0.0.1:50000"

	req.Header.Set(
		"X-Forwarded-For",
		"203.0.113.20, invalid-ip",
	)

	got := resolver.ClientIP(req)

	if got != "127.0.0.1" {
		t.Fatalf(
			"expected proxy RemoteAddr fallback, got %q",
			got,
		)
	}
}

func TestClientIPIPv6(
	t *testing.T,
) {
	resolver, err := NewClientIPResolver(
		"::1/128",
		true,
	)

	if err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(
		"GET",
		"/",
		nil,
	)

	req.RemoteAddr = "[::1]:50000"

	req.Header.Set(
		"CF-Connecting-IP",
		"2001:db8::1234",
	)

	got := resolver.ClientIP(req)

	if got != "2001:db8::1234" {
		t.Fatalf(
			"expected IPv6 client, got %q",
			got,
		)
	}
}

func TestClientIPInvalidTrustedProxyCIDR(
	t *testing.T,
) {
	_, err := NewClientIPResolver(
		"not-a-cidr",
		false,
	)

	if err == nil {
		t.Fatal(
			"expected invalid CIDR to fail",
		)
	}
}
