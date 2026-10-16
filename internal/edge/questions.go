package edge

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// While it runs, the edge needs the panel's say on two things: whether a
// name may have an on-demand certificate, and the DNS records of DNS-01
// challenges, which the panel's DNS module serves. The edge never calls
// the panel. In one process the edge asks a Responder directly; between
// processes the panel holds a long poll on the edge's admin socket and
// answers the questions it gets.

// Question kinds.
const (
	AskCertificate = "certificate"
	AskPresentDNS  = "dns-present"
	AskCleanUpDNS  = "dns-cleanup"
)

// DNSRecord is a TXT record of a DNS-01 challenge.
type DNSRecord struct {
	// Name is relative to the zone, such as _acme-challenge.www.
	Name  string        `json:"name"`
	Type  string        `json:"type"`
	Value string        `json:"value"`
	TTL   time.Duration `json:"ttl,omitempty"`
}

// Question is something the edge asks the panel.
type Question struct {
	ID   string `json:"id"`
	Kind string `json:"kind" enum:"certificate,dns-present,dns-cleanup"`
	// Name is the name a certificate would be for.
	Name string `json:"name,omitempty"`
	// Zone and Record are a DNS-01 challenge's record.
	Zone   string     `json:"zone,omitempty"`
	Record *DNSRecord `json:"record,omitempty"`
}

// Answer answers a question. An empty error means yes, or done.
type Answer struct {
	ID    string `json:"id"`
	Error string `json:"error,omitempty"`
	// Denied tells a refusal from a failure to decide.
	Denied bool `json:"denied,omitempty"`
}

// Responder answers the edge's questions on the panel's side.
type Responder interface {
	// AllowCertificate returns nil when name may have an on-demand
	// certificate, and an error wrapping ErrDenied when it may not.
	AllowCertificate(ctx context.Context, name string) error
	// PresentRecord publishes a DNS-01 challenge record in zone.
	PresentRecord(ctx context.Context, zone string, rec DNSRecord) error
	// CleanUpRecord removes it again.
	CleanUpRecord(ctx context.Context, zone string, rec DNSRecord) error
}

// Errors of questions.
var (
	// ErrDenied is a refusal.
	ErrDenied = errors.New("the panel refused")
	// ErrNoPanel means no panel is answering questions.
	ErrNoPanel = errors.New("the panel isn't answering the edge's questions")
	// ErrNoDNS means no DNS module serves challenge records.
	ErrNoDNS = errors.New("no DNS service publishes challenge records")
)

// ask answers one question with a Responder.
func ask(ctx context.Context, r Responder, q Question) error {
	switch q.Kind {
	case AskCertificate:
		return r.AllowCertificate(ctx, q.Name)
	case AskPresentDNS, AskCleanUpDNS:
		if q.Record == nil {
			return errors.New("the question has no record")
		}
		if q.Kind == AskPresentDNS {
			return r.PresentRecord(ctx, q.Zone, *q.Record)
		}
		return r.CleanUpRecord(ctx, q.Zone, *q.Record)
	}
	return fmt.Errorf("unknown question %q", q.Kind)
}

// answerFor turns a Responder's result into an Answer.
func answerFor(id string, err error) Answer {
	a := Answer{ID: id}
	if err != nil {
		a.Error, a.Denied = err.Error(), errors.Is(err, ErrDenied)
	}
	return a
}

// errorFor turns an Answer back into an error.
func errorFor(a Answer) error {
	switch {
	case a.Error == "":
		return nil
	case a.Denied:
		return fmt.Errorf("%w: %s", ErrDenied, a.Error)
	}
	return errors.New(a.Error)
}

// pollGrace is how long after its last poll the panel counts as
// listening, so a question asked between two polls waits for the next one.
const pollGrace = 90 * time.Second

// questionBroker holds the questions waiting for the panel's next poll.
type questionBroker struct {
	mu       sync.Mutex
	pending  map[string]*pendingQuestion
	queue    []string
	wake     chan struct{}
	lastPoll time.Time
	now      func() time.Time
}

type pendingQuestion struct {
	q    Question
	done chan Answer
}

func newBroker() *questionBroker {
	return &questionBroker{pending: map[string]*pendingQuestion{}, wake: make(chan struct{}), now: time.Now}
}

// ask queues a question and waits for its answer until ctx is done.
func (b *questionBroker) ask(ctx context.Context, q Question) error {
	var id [12]byte
	rand.Read(id[:])
	q.ID = hex.EncodeToString(id[:])
	p := &pendingQuestion{q: q, done: make(chan Answer, 1)}
	b.mu.Lock()
	if b.lastPoll.IsZero() || b.now().Sub(b.lastPoll) > pollGrace {
		b.mu.Unlock()
		return ErrNoPanel
	}
	b.pending[q.ID] = p
	b.queue = append(b.queue, q.ID)
	close(b.wake)
	b.wake = make(chan struct{})
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.pending, q.ID)
		b.mu.Unlock()
	}()
	select {
	case a := <-p.done:
		return errorFor(a)
	case <-ctx.Done():
		return fmt.Errorf("waiting for the panel's answer: %w", ctx.Err())
	}
}

// poll returns the questions not handed out yet, waiting up to wait for
// one to arrive.
func (b *questionBroker) poll(ctx context.Context, wait time.Duration) []Question {
	deadline := time.NewTimer(wait)
	defer deadline.Stop()
	for {
		b.mu.Lock()
		b.lastPoll = b.now()
		var out []Question
		for _, id := range b.queue {
			if p, ok := b.pending[id]; ok {
				out = append(out, p.q)
			}
		}
		b.queue = nil
		wake := b.wake
		b.mu.Unlock()
		if len(out) > 0 {
			return out
		}
		select {
		case <-wake:
		case <-deadline.C:
			// The poll ends; the panel counts as listening until the grace
			// after this moment runs out.
			b.mu.Lock()
			b.lastPoll = b.now()
			b.mu.Unlock()
			return nil
		case <-ctx.Done():
			return nil
		}
	}
}

// answer delivers answers; unknown IDs, such as questions that timed out,
// are ignored.
func (b *questionBroker) answer(answers []Answer) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, a := range answers {
		if p, ok := b.pending[a.ID]; ok {
			select {
			case p.done <- a:
			default:
			}
		}
	}
}

// Answers is the panel's Responder. Modules plug in what they know: which
// names may have on-demand certificates, and the DNS service that
// publishes challenge records.
type Answers struct {
	mu        sync.RWMutex
	approvers map[int]func(ctx context.Context, name string) (bool, error)
	next      int
	dns       DNSChallenger
}

// DNSChallenger publishes the TXT records of DNS-01 challenges in the
// zones it serves, such as the panel's DNS module.
type DNSChallenger interface {
	PresentRecord(ctx context.Context, zone string, rec DNSRecord) error
	CleanUpRecord(ctx context.Context, zone string, rec DNSRecord) error
}

// DefaultAnswers is the panel's Responder that modules register with.
var DefaultAnswers = &Answers{}

// ApproveNames adds a check that approves names for on-demand
// certificates, such as the custom domains of a module's resources. A name
// is approved when any check approves it. Call remove to take it out.
func (a *Answers) ApproveNames(check func(ctx context.Context, name string) (bool, error)) (remove func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.approvers == nil {
		a.approvers = map[int]func(context.Context, string) (bool, error){}
	}
	id := a.next
	a.next++
	a.approvers[id] = check
	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		delete(a.approvers, id)
	}
}

// SetDNSChallenger sets the service that publishes challenge records.
// Call remove when it stops.
func (a *Answers) SetDNSChallenger(c DNSChallenger) (remove func()) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.dns = c
	return func() {
		a.mu.Lock()
		defer a.mu.Unlock()
		if a.dns == c {
			a.dns = nil
		}
	}
}

// AllowCertificate implements Responder.
func (a *Answers) AllowCertificate(ctx context.Context, name string) error {
	a.mu.RLock()
	checks := make([]func(context.Context, string) (bool, error), 0, len(a.approvers))
	for _, c := range a.approvers {
		checks = append(checks, c)
	}
	a.mu.RUnlock()
	var errs []error
	for _, check := range checks {
		ok, err := check(ctx, name)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if ok {
			return nil
		}
	}
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return fmt.Errorf("%w: no module approves a certificate for %s", ErrDenied, name)
}

func (a *Answers) challenger() (DNSChallenger, error) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.dns == nil {
		return nil, ErrNoDNS
	}
	return a.dns, nil
}

// PresentRecord implements Responder.
func (a *Answers) PresentRecord(ctx context.Context, zone string, rec DNSRecord) error {
	c, err := a.challenger()
	if err != nil {
		return err
	}
	return c.PresentRecord(ctx, zone, rec)
}

// CleanUpRecord implements Responder.
func (a *Answers) CleanUpRecord(ctx context.Context, zone string, rec DNSRecord) error {
	c, err := a.challenger()
	if err != nil {
		return err
	}
	return c.CleanUpRecord(ctx, zone, rec)
}

// AnswerQuestions answers the questions of an edge in another process
// until ctx is done, polling its admin socket. The panel runs it next to
// its edge client.
func AnswerQuestions(ctx context.Context, c *AdminClient, r Responder, log *slog.Logger) error {
	if log == nil {
		log = slog.Default()
	}
	// Each question is answered on its own, so a slow DNS update doesn't
	// hold up a quick certificate check.
	busy := make(chan struct{}, 16)
	var wg sync.WaitGroup
	defer wg.Wait()
	backoff := time.Second
	for ctx.Err() == nil {
		questions, err := c.Questions(ctx, 25*time.Second)
		if err != nil {
			if ctx.Err() != nil {
				break
			}
			// An edge that isn't running yet starts on this request, since
			// systemd holds its socket.
			log.Debug("polling the edge for questions", "error", err)
			select {
			case <-ctx.Done():
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
		for _, q := range questions {
			select {
			case busy <- struct{}{}:
			case <-ctx.Done():
				return nil
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer func() { <-busy }()
				qctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
				defer cancel()
				err := ask(qctx, r, q)
				if err != nil && !errors.Is(err, ErrDenied) {
					log.Warn("answering the edge", "question", q.Kind, "name", strings.TrimSpace(q.Name+" "+q.Zone), "error", err)
				}
				if err := c.Answer(ctx, []Answer{answerFor(q.ID, err)}); err != nil && ctx.Err() == nil {
					log.Warn("answering the edge", "error", err)
				}
			}()
		}
	}
	return nil
}
