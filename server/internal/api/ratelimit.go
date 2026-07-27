package api

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"
)

const (
	// Idle buckets are dropped after this long. At any configured rate a
	// bucket refills to full well before that, so eviction never costs a
	// returning client tokens it was still owed.
	bucketIdleTTL = 10 * time.Minute
	sweepEvery    = time.Minute
	// Hard cap on tracked addresses; when a large botnet pushes the map past
	// this, sweeping runs on every call until it shrinks, so memory stays
	// bounded even if the timer-based sweep cannot keep up.
	maxBuckets = 1 << 17
)

// ipLimiter is a token-bucket rate limiter keyed by client address. Every
// query on this API is answered from a single SQLite connection, and a
// keyword search is two full-table scans — so a cheap, unauthenticated flood
// of GETs is enough to starve the crawler and every other client. This
// per-client bucket is the first line of defense; the search handler's
// admission gate (see api.go) is the second.
type ipLimiter struct {
	rate  float64 // tokens added per second
	burst float64 // bucket capacity

	mu        sync.Mutex
	buckets   map[string]*bucket
	lastSweep time.Time
	now       func() time.Time // injectable clock for tests
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newIPLimiter(rate float64, burst int) *ipLimiter {
	return &ipLimiter{
		rate:    rate,
		burst:   float64(burst),
		buckets: make(map[string]*bucket),
		now:     time.Now,
	}
}

// allow reports whether the client identified by key may proceed, consuming
// one token if so.
func (l *ipLimiter) allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	if now.Sub(l.lastSweep) >= sweepEvery || len(l.buckets) > maxBuckets {
		l.sweep(now)
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	} else {
		b.tokens = min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.rate)
		b.last = now
	}
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

func (l *ipLimiter) sweep(now time.Time) {
	l.lastSweep = now
	for k, b := range l.buckets {
		if now.Sub(b.last) > bucketIdleTTL {
			delete(l.buckets, k)
		}
	}
}

// clientKey returns the rate-limit key for a request: the client address,
// resolved through forwarding headers only when the direct peer is itself
// trusted infrastructure (loopback or private — the reverse proxy or the
// Next.js SSR process on the same box). A public peer's forwarding headers
// are attacker-controlled and ignored, so hitting the API directly with a
// spoofed X-Forwarded-For cannot borrow someone else's bucket.
func clientKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	if trustedPeer(peer) {
		if fwd, ok := forwardedClient(r); ok {
			return bucketKey(fwd)
		}
	}
	return bucketKey(peer)
}

func trustedPeer(a netip.Addr) bool {
	a = a.Unmap()
	return a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast()
}

// forwardedClient extracts the client from X-Forwarded-For by walking right
// to left and skipping trusted hops: each proxy appends the peer it saw, so
// the rightmost untrusted address is the one our own infrastructure vouches
// for. Anything left of it came from the client and is spoofable. Malformed
// entries stop the walk rather than let garbage promote a spoofed hop.
func forwardedClient(r *http.Request) (netip.Addr, bool) {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		hops := strings.Split(xff, ",")
		for i := len(hops) - 1; i >= 0; i-- {
			a, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
			if err != nil {
				break
			}
			if !trustedPeer(a) {
				return a.Unmap(), true
			}
		}
	}
	if xr := r.Header.Get("X-Real-IP"); xr != "" {
		if a, err := netip.ParseAddr(strings.TrimSpace(xr)); err == nil {
			return a.Unmap(), true
		}
	}
	return netip.Addr{}, false
}

// bucketKey collapses IPv6 clients to their /64: a single host routinely
// owns a whole /64, so per-address buckets would hand an IPv6 attacker
// effectively unlimited fresh buckets.
func bucketKey(a netip.Addr) string {
	a = a.Unmap().WithZone("")
	if a.Is4() {
		return a.String()
	}
	return netip.PrefixFrom(a, 64).Masked().String()
}
