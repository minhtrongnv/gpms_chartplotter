package server

import (
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// ClientIPResolver determines the real client IP while preventing
// spoofed proxy headers.
//
// Direct requests always use RemoteAddr.
//
// Proxy headers are considered only when RemoteAddr belongs to one
// of the configured trusted proxy CIDRs.
type ClientIPResolver struct {
	trustedProxies  []netip.Prefix
	trustCloudflare bool
}

func NewClientIPResolver(
	trustedProxyCIDRs string,
	trustCloudflare bool,
) (*ClientIPResolver, error) {
	resolver := &ClientIPResolver{
		trustCloudflare: trustCloudflare,
	}

	trustedProxyCIDRs = strings.TrimSpace(
		trustedProxyCIDRs,
	)

	if trustedProxyCIDRs == "" {
		return resolver, nil
	}

	for _, raw := range strings.Split(
		trustedProxyCIDRs,
		",",
	) {
		raw = strings.TrimSpace(raw)

		if raw == "" {
			continue
		}

		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			return nil, fmt.Errorf(
				"invalid trusted proxy CIDR %q: %w",
				raw,
				err,
			)
		}

		resolver.trustedProxies = append(
			resolver.trustedProxies,
			prefix.Masked(),
		)
	}

	return resolver, nil
}

// trustedPeer reports whether RemoteAddr belongs to an explicitly configured
// reverse-proxy CIDR. It is deliberately based only on the TCP peer address; proxy
// headers are not consulted here.
func (c *ClientIPResolver) trustedPeer(remoteAddr string) bool {
	if c == nil {
		return false
	}
	ip, ok := remoteAddrIP(remoteAddr)
	return ok && c.isTrustedProxy(ip)
}

// ClientIP returns the best-known client IP.
//
// Security rules:
//
//   - Direct/untrusted peer:
//     always use RemoteAddr.
//
//   - Trusted Cloudflare proxy:
//     prefer CF-Connecting-IP.
//
//   - Trusted generic proxy:
//     inspect X-Forwarded-For from right to left and return
//     the first untrusted address.
//
//   - Invalid/malformed proxy headers:
//     fall back to RemoteAddr.
func (c *ClientIPResolver) ClientIP(
	r *http.Request,
) string {
	remoteIP, ok := remoteAddrIP(r.RemoteAddr)
	if !ok {
		return ""
	}

	if !c.isTrustedProxy(remoteIP) {
		return remoteIP.String()
	}

	if c.trustCloudflare {
		if ip, ok := parseSingleIP(
			r.Header.Get("CF-Connecting-IP"),
		); ok {
			return ip.String()
		}
	}

	if ip, ok := c.clientFromXForwardedFor(
		r.Header.Get("X-Forwarded-For"),
	); ok {
		return ip.String()
	}

	return remoteIP.String()
}

func (c *ClientIPResolver) clientFromXForwardedFor(
	header string,
) (netip.Addr, bool) {
	header = strings.TrimSpace(header)

	if header == "" {
		return netip.Addr{}, false
	}

	parts := strings.Split(header, ",")

	addresses := make(
		[]netip.Addr,
		0,
		len(parts),
	)

	// Treat the whole X-Forwarded-For value as invalid if
	// any element is malformed. This is deliberately
	// conservative for rate-limit/security use.
	for _, part := range parts {
		ip, ok := parseSingleIP(part)
		if !ok {
			return netip.Addr{}, false
		}

		addresses = append(
			addresses,
			ip,
		)
	}

	// Proxies append addresses from left → right.
	// Walk backwards and discard addresses belonging to
	// explicitly trusted proxies.
	for i := len(addresses) - 1; i >= 0; i-- {
		ip := addresses[i]

		if !c.isTrustedProxy(ip) {
			return ip, true
		}
	}

	return netip.Addr{}, false
}

func (c *ClientIPResolver) isTrustedProxy(
	ip netip.Addr,
) bool {
	ip = ip.Unmap()

	for _, prefix := range c.trustedProxies {
		if prefix.Contains(ip) {
			return true
		}
	}

	return false
}

func remoteAddrIP(
	remoteAddr string,
) (netip.Addr, bool) {
	remoteAddr = strings.TrimSpace(remoteAddr)

	if remoteAddr == "" {
		return netip.Addr{}, false
	}

	host, _, err := net.SplitHostPort(remoteAddr)

	if err == nil {
		return parseSingleIP(host)
	}

	// Useful for tests and callers that provide a raw IP
	// without a port.
	return parseSingleIP(remoteAddr)
}

func parseSingleIP(
	value string,
) (netip.Addr, bool) {
	value = strings.TrimSpace(value)

	if value == "" {
		return netip.Addr{}, false
	}

	ip, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Addr{}, false
	}

	return ip.Unmap(), true
}
