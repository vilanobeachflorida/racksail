package api

import (
	"math"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
)

// RateLimit bounds how many requests a minute callers may make: each
// signed-in user and each API token, and each address without credentials.
// Zero turns a limit off.
type RateLimit struct {
	PerActor   int
	PerAddress int
}

// limiter is a token bucket per caller. A bucket holds a quarter of a
// minute's requests and ten more, so pages that load several things at once
// aren't held up, and refills at the steady rate.
type limiter struct {
	limits RateLimit
	now    func() time.Time

	mu      sync.Mutex
	buckets map[string]*bucket
	swept   time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(limits RateLimit, now func() time.Time) *limiter {
	if now == nil {
		now = time.Now
	}
	return &limiter{limits: limits, now: now, buckets: map[string]*bucket{}}
}

// key names the bucket of a request: its actor when it has one, or its
// address. IPv6 addresses share a bucket per /64, which is what one machine
// usually has.
func (l *limiter) key(actor *auth.Actor, remote string) (string, int) {
	if actor != nil {
		who := string(actor.ID)
		if who == "" {
			who = actor.Name
		}
		return "actor:" + string(actor.Type) + ":" + who, l.limits.PerActor
	}
	addr, err := netip.ParseAddrPort(remote)
	if err != nil {
		return "address:" + remote, l.limits.PerAddress
	}
	ip := addr.Addr().Unmap()
	if ip.Is6() {
		ip = netip.PrefixFrom(ip, 64).Masked().Addr()
	}
	return "address:" + ip.String(), l.limits.PerAddress
}

// allow takes a token for the caller. When there is none it reports how
// long until there will be.
func (l *limiter) allow(actor *auth.Actor, remote string) (ok bool, limit, remaining int, wait time.Duration) {
	key, limit := l.key(actor, remote)
	if limit <= 0 {
		return true, 0, 0, 0
	}
	now := l.now()
	capacity := float64(limit)/4 + 10
	rate := float64(limit) / 60
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)
	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: capacity, last: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(capacity, b.tokens+now.Sub(b.last).Seconds()*rate)
	b.last = now
	if b.tokens < 1 {
		return false, limit, 0, time.Duration((1 - b.tokens) / rate * float64(time.Second))
	}
	b.tokens--
	return true, limit, int(b.tokens), 0
}

// sweep forgets, every few minutes, the buckets of callers that have been
// quiet long enough for them to be full again, so callers that went away
// don't keep memory. l.mu must be held.
func (l *limiter) sweep(now time.Time) {
	if now.Sub(l.swept) < 5*time.Minute {
		return
	}
	l.swept = now
	for key, b := range l.buckets {
		if now.Sub(b.last) > 10*time.Minute {
			delete(l.buckets, key)
		}
	}
}

// retryAfter rounds a wait up to whole seconds, for the Retry-After header.
func retryAfter(wait time.Duration) string {
	return strconv.Itoa(int(math.Ceil(wait.Seconds())))
}
