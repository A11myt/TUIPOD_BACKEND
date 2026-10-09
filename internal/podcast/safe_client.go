package podcast

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// safeHTTPClient is shared by every outbound fetch this package makes
// against a URL that ultimately comes from a user (the RSS URL itself on
// fetch/refresh/OPML-import, and the <image> URL a fetched feed's own body
// names, used by fetchImageValidators) — without it, an authenticated user
// could point either fetch at http://localhost, an internal service, or a
// cloud metadata endpoint (e.g. Fly.io/AWS's 169.254.169.254) and have this
// server make that request on their behalf (SSRF). Guarded at *dial* time,
// not just URL-parse time: validating the parsed hostname alone is
// vulnerable to DNS rebinding (a hostname that resolves to a public IP when
// checked but a private one moments later, when the fetch actually
// connects) — resolving and checking the IP happen in the same dial call
// here, and the dialer connects to that already-checked IP directly rather
// than re-resolving the hostname a second time.
var safeHTTPClient = &http.Client{
	Timeout: 15 * time.Second,
	Transport: &http.Transport{
		DialContext: safeDialContext,
	},
}

func safeDialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}

	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, err
	}

	var target net.IP
	for _, ip := range ips {
		if isDisallowedIP(ip) {
			continue
		}
		target = ip
		break
	}
	if target == nil {
		return nil, fmt.Errorf("refusing to fetch %s: no public IP address to connect to", host)
	}

	dialer := &net.Dialer{Timeout: 10 * time.Second}
	return dialer.DialContext(ctx, network, net.JoinHostPort(target.String(), port))
}

// AllowPrivateNetworksForTests disables the guard below — exported only so
// this package's own tests can point safeHTTPClient at an
// httptest.NewServer (which always listens on loopback). Set via this
// package's TestMain, nowhere else; never true in non-test code.
var AllowPrivateNetworksForTests = false

// isDisallowedIP blocks loopback, RFC1918/RFC4193 private, link-local
// (including the 169.254.169.254 cloud metadata address), unspecified, and
// multicast ranges — everything that isn't a normal public address a podcast
// host would legitimately serve a feed/image from.
func isDisallowedIP(ip net.IP) bool {
	if AllowPrivateNetworksForTests {
		return false
	}
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified() ||
		ip.IsMulticast()
}
