package identity

import (
	"math"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Sign-in attempts from one address: a burst of 20, then one every six
// seconds. The API's own rate limit is far looser; this one is what makes
// guessing passwords and codes slow, together with lockouts per user.
const (
	attemptBurst = 20
	attemptEvery = 6 * time.Second
)

// attempts limits how often each address may try to sign in, set up the
// first admin, use a sign-in link or confirm it's someone.
type attempts struct {
	now func() time.Time
	mu  sync.Mutex
	// buckets are keyed by address, IPv6 by its /64, since one machine
	// often has a whole /64.
	buckets map[string]*bucket
}

type bucket struct {
	tokens float64
	at     time.Time
}

func newAttempts(now func() time.Time) *attempts {
	return &attempts{now: now, buckets: map[string]*bucket{}}
}

func addressKey(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ip
	}
	a = a.Unmap()
	if a.Is6() {
		p, _ := a.Prefix(64)
		return p.String()
	}
	return a.String()
}

// take uses up one attempt for ip, or says how long to wait for the next.
func (l *attempts) take(ip string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	key := addressKey(ip)
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: attemptBurst, at: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(attemptBurst, b.tokens+now.Sub(b.at).Seconds()/attemptEvery.Seconds())
	b.at = now
	if b.tokens < 1 {
		wait := time.Duration((1 - b.tokens) * float64(attemptEvery))
		return i18n.ErrRateLimited.New("retryAfter", strconv.Itoa(int(math.Ceil(wait.Seconds()))))
	}
	b.tokens--
	return nil
}

// prune forgets addresses whose buckets are full again.
func (l *attempts) prune() {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	for k, b := range l.buckets {
		if b.tokens+now.Sub(b.at).Seconds()/attemptEvery.Seconds() >= attemptBurst {
			delete(l.buckets, k)
		}
	}
}

// challenge is a step of signing in that waits for the person: the second
// step after a password, or a passkey ceremony.
type challenge struct {
	user store.ID
	// purpose says what the challenge is for: signin, passkey-signin,
	// passkey-register, passkey-step-up or mfa.
	purpose    string
	method     string
	persistent bool
	expires    time.Time
	// data is what the ceremony keeps between its two halves, such as
	// WebAuthn session data or an add-on's MFA state.
	data []byte
	// failures counts wrong codes, so one challenge can't be used to guess
	// forever.
	failures int
}

const (
	challengeTTL         = 5 * time.Minute
	challengeMaxFailures = 5
)

// challenges keeps what waits for people, by the hash of the challenge's
// secret, in memory: a sign-in that is half done when the panel restarts
// starts again.
type challenges struct {
	now func() time.Time
	mu  sync.Mutex
	m   map[string]*challenge
}

func newChallenges(now func() time.Time) *challenges {
	return &challenges{now: now, m: map[string]*challenge{}}
}

// put keeps c and returns the secret that finds it again.
func (cs *challenges) put(c *challenge) string {
	secret := auth.NewSecret("")
	cs.mu.Lock()
	defer cs.mu.Unlock()
	if c.expires.IsZero() {
		c.expires = cs.now().Add(challengeTTL)
	}
	cs.m[auth.HashSecret(secret)] = c
	return secret
}

// get returns the challenge of a secret, for one purpose.
func (cs *challenges) get(secret, purpose string) (*challenge, error) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	key := auth.HashSecret(secret)
	c, ok := cs.m[key]
	if !ok || c.purpose != purpose || cs.now().After(c.expires) {
		delete(cs.m, key)
		return nil, auth.ErrChallengeExpired.New()
	}
	return c, nil
}

// fail counts a wrong answer and drops the challenge after too many.
func (cs *challenges) fail(secret string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	key := auth.HashSecret(secret)
	if c, ok := cs.m[key]; ok {
		c.failures++
		if c.failures >= challengeMaxFailures {
			delete(cs.m, key)
		}
	}
}

func (cs *challenges) drop(secret string) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	delete(cs.m, auth.HashSecret(secret))
}

func (cs *challenges) dropUser(user store.ID) {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	for k, c := range cs.m {
		if c.user == user {
			delete(cs.m, k)
		}
	}
}

func (cs *challenges) prune() {
	cs.mu.Lock()
	defer cs.mu.Unlock()
	now := cs.now()
	for k, c := range cs.m {
		if now.After(c.expires) {
			delete(cs.m, k)
		}
	}
}
