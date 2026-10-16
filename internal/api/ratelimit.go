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
	// refused counts the requests refused since the panel started.
	refused int64
}

// quota is where a caller stands: how many requests their bucket holds when
// full, how many it holds now, and how long it takes to fill up again.
type quota struct {
	limit, remaining int
	reset            time.Duration
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

// capacity is how many requests a bucket of a per-minute limit holds.
func capacity(limit int) int { return limit/4 + 10 }

// allow takes a token for the caller and says where they stand. When there
// is no token it reports how long until there will be. A zero quota means
// the caller isn't limited.
func (l *limiter) allow(actor *auth.Actor, remote string) (ok bool, q quota, wait time.Duration) {
	key, limit := l.key(actor, remote)
	if limit <= 0 {
		return true, quota{}, 0
	}
	now := l.now()
	full := float64(capacity(limit))
	rate := float64(limit) / 60
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sweep(now)
	b := l.buckets[key]
	if b == nil {
		b = &bucket{tokens: full, last: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(full, b.tokens+now.Sub(b.last).Seconds()*rate)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		ok = true
	} else {
		l.refused++
		wait = time.Duration((1 - b.tokens) / rate * float64(time.Second))
	}
	q = quota{limit: int(full), remaining: int(b.tokens), reset: time.Duration((full - b.tokens) / rate * float64(time.Second))}
	return ok, q, wait
}

// stats returns how many requests were refused since the panel started and
// how many callers have buckets now.
func (l *limiter) stats() (refused int64, callers int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.refused, len(l.buckets)
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

// retryAfter rounds a wait up to whole seconds, for the Retry-After and
// RateLimit-Reset headers.
func retryAfter(wait time.Duration) string {
	return strconv.Itoa(int(math.Ceil(wait.Seconds())))
}
