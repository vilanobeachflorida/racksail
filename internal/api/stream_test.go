package api

import (
	"bufio"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/jobs"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// open starts a streaming request as an actor.
func (s *stack) open(actor, path string, header ...string) *http.Response {
	s.t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	s.t.Cleanup(cancel)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.http.URL+path, nil)
	if err != nil {
		s.t.Fatal(err)
	}
	req.Header.Set("X-Test-Actor", actor)
	for i := 0; i+1 < len(header); i += 2 {
		req.Header.Set(header[i], header[i+1])
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		s.t.Fatal(err)
	}
	s.t.Cleanup(func() { resp.Body.Close() })
	return resp
}

func TestEventStream(t *testing.T) {
	s := newStack(t, RateLimit{})
	ctx := context.Background()
	acct := store.AccountScope(s.account)
	resp := s.open("owner", "/api/v1/events/stream?type=test.*")
	if resp.StatusCode != http.StatusOK || resp.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream = %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	// Events of other owners and of other types don't reach this owner.
	for _, e := range []events.Event{
		{Type: "test.site.created", Scope: store.AccountScope(store.NewID("acct"))},
		{Type: "dns.zone.created", Scope: acct},
		{Type: "test.site.created", Scope: acct, Message: "mine"},
	} {
		if err := s.log.Publish(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	var first events.Event
	var firstID string
	readEvents(t, resp, func(event, id, data string) bool {
		json.Unmarshal([]byte(data), &first)
		firstID = id
		return true
	})
	if first.Message != "mine" || firstID != string(first.ID) {
		t.Fatalf("first event = %+v (id %s)", first, firstID)
	}

	// A client that reconnects gets what it missed.
	for _, msg := range []string{"missed one", "missed two"} {
		if err := s.log.Publish(ctx, events.Event{Type: "test.site.updated", Scope: acct, Message: msg}); err != nil {
			t.Fatal(err)
		}
	}
	again := s.open("owner", "/api/v1/events/stream?type=test.*", "Last-Event-ID", firstID)
	var got []string
	readEvents(t, again, func(event, id, data string) bool {
		var e events.Event
		json.Unmarshal([]byte(data), &e)
		got = append(got, e.Message)
		return len(got) == 2
	})
	if strings.Join(got, ",") != "missed one,missed two" {
		t.Errorf("replayed = %v", got)
	}
	if resp := s.call("nobody", "GET", "/api/v1/events/stream", nil, nil); resp.StatusCode != http.StatusForbidden {
		t.Errorf("without a grant = %d", resp.StatusCode)
	}
}

// A client too slow for the subscription's buffer still gets every event,
// once, in order.
func TestEventStreamCatchesUp(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	log := events.NewMemory(5000)
	gate := make(chan struct{})
	got := make(chan string, 2000)
	var once bool
	out := func(event, id string, data any) error {
		if !once {
			once = true
			<-gate
		}
		got <- data.(events.Event).Message
		return nil
	}
	ready := make(chan struct{})
	go followEvents(ctx, log, events.Filter{Types: []string{"test.*"}}, 0, out, func() error { close(ready); return nil })
	<-ready
	const n = 1000
	for i := range n {
		if err := log.Publish(ctx, events.Event{Type: "test.thing.happened", Message: strconv.Itoa(i)}); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			// The stream is now stuck sending the first event while the
			// rest overflow its buffer.
			time.Sleep(20 * time.Millisecond)
		}
	}
	close(gate)
	for i := range n {
		select {
		case msg := <-got:
			if msg != strconv.Itoa(i) {
				t.Fatalf("event %d is %s", i, msg)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d events arrived", i, n)
		}
	}
	select {
	case msg := <-got:
		t.Errorf("an event arrived twice: %s", msg)
	case <-time.After(50 * time.Millisecond):
	}
}

// A client that fell behind the resource stream is told to read its lists
// again, and then gets the notices that came after.
func TestResourceStreamResync(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	kinds := store.NewKindRegistry()
	if err := kinds.Register(noteKind); err != nil {
		t.Fatal(err)
	}
	st := store.NewMemory(kinds)
	create := func() {
		r := &store.Resource{Meta: store.Meta{Kind: "test.note", Scope: store.AccountScope(store.NewID("acct"))}, Spec: json.RawMessage(`{"text":"x"}`)}
		if err := st.Write(ctx, store.WriteMeta{}, func(tx store.Tx) error { return tx.Create(ctx, r) }); err != nil {
			t.Fatal(err)
		}
	}
	w := st.Watch(store.WatchFilter{}, 2)
	defer w.Close()
	gate := make(chan struct{})
	got := make(chan string, 64)
	first := true
	go followResources(ctx, w, func(store.Notice) bool { return true }, func(event, id string, data any) error {
		if first {
			first = false
			<-gate
		}
		got <- event
		return nil
	})
	create()
	time.Sleep(20 * time.Millisecond)
	// The stream is stuck on the first notice; two fit in the buffer and
	// the rest are lost.
	for range 5 {
		create()
	}
	close(gate)
	var seq []string
	for len(seq) < 4 {
		select {
		case e := <-got:
			seq = append(seq, e)
		case <-time.After(5 * time.Second):
			t.Fatalf("events = %v", seq)
		}
	}
	if strings.Join(seq, ",") != "notice,resync,notice,notice" {
		t.Errorf("events = %v", seq)
	}
	create()
	if e := <-got; e != "notice" {
		t.Errorf("after catching up = %s", e)
	}
}

func TestJobStream(t *testing.T) {
	s := newStack(t, RateLimit{})
	var cs changeset.ChangeSet
	resp := s.call("owner", "POST", "/api/v1/test/sites", map[string]any{
		"scope": map[string]any{"level": "account", "id": s.account}, "spec": map[string]any{"domains": []string{"stream.test"}, "content": "x"},
	}, &cs)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatal(bodyOf(resp))
	}
	stream := s.open("owner", "/api/v1/jobs/"+string(cs.Job)+"/stream")
	var lines int
	var last jobs.Job
	readEvents(t, stream, func(event, id, data string) bool {
		switch event {
		case "log":
			lines++
			if id == "" {
				t.Error("log lines carry their number as their ID")
			}
		case "job":
			json.Unmarshal([]byte(data), &last)
		}
		return last.State.Done()
	})
	if last.State != jobs.StateSucceeded || lines == 0 {
		t.Errorf("the stream ends with the finished job: %+v after %d lines", last, lines)
	}
	// A stream of a job that already finished sends its state and log and
	// ends.
	again := s.open("owner", "/api/v1/jobs/"+string(cs.Job)+"/stream", "Last-Event-ID", "1")
	var replayed int
	readEvents(t, again, func(event, id, data string) bool {
		if event == "log" {
			replayed++
			if id == "1" {
				t.Error("lines up to Last-Event-ID aren't sent again")
			}
		}
		return false
	})
	if replayed != lines-1 {
		t.Errorf("replayed %d of %d lines", replayed, lines)
	}
	if resp := s.call("stranger", "GET", "/api/v1/jobs/"+string(cs.Job)+"/stream", nil, nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("another's job = %d", resp.StatusCode)
	}
}

func TestResourceStream(t *testing.T) {
	s := newStack(t, RateLimit{})
	mine := s.open("owner", "/api/v1/resources/stream?kind=test.site")
	theirs := s.open("stranger", "/api/v1/resources/stream?kind=test.site")
	var cs changeset.ChangeSet
	s.call("owner", "POST", "/api/v1/test/sites", map[string]any{
		"scope": map[string]any{"level": "account", "id": s.account}, "spec": map[string]any{"domains": []string{"watched.test"}, "content": "x"},
	}, &cs)
	var phases []store.Phase
	readEvents(t, mine, func(event, id, data string) bool {
		var n store.Notice
		json.Unmarshal([]byte(data), &n)
		if n.ID != cs.Changes[0].ID {
			t.Errorf("notice = %+v", n)
		}
		phases = append(phases, n.Phase)
		return n.Phase == store.PhaseReady
	})
	if phases[0] != store.PhasePending {
		t.Errorf("phases = %v", phases)
	}
	// The other owner's stream stays quiet, apart from its greeting.
	got := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(theirs.Body)
		for sc.Scan() {
			if line := sc.Text(); strings.HasPrefix(line, "data: ") {
				got <- line
			}
		}
	}()
	select {
	case data := <-got:
		t.Errorf("another account's owner was told: %s", data)
	case <-time.After(200 * time.Millisecond):
	}
	if resp := s.call("owner", "GET", "/api/v1/resources/stream?kind=nope.thing", nil, nil); resp.StatusCode != http.StatusUnprocessableEntity {
		t.Errorf("an unknown kind = %d", resp.StatusCode)
	}
}

// chatty is a job that reports progress around every line it writes: it
// waits for start, writes its first lines, says so on paused when that
// isn't nil and waits for more, then writes the rest.
type chatty struct {
	start, more <-chan struct{}
	paused      chan<- struct{}
}

// register registers the job type on the stack's queue.
func (c chatty) register(t *testing.T, s *stack, name string) {
	t.Helper()
	write := func(r jobs.Reporter, from, to, total int) {
		for i := from; i <= to; i++ {
			r.Progress(int64(i), int64(total), "working")
			r.Log(slog.LevelInfo, "line "+strconv.Itoa(i))
			r.Progress(int64(i), int64(total), "still working")
		}
	}
	err := s.jobs.Handle(jobs.Type{Name: name, Run: func(ctx context.Context, job *jobs.Job, args json.RawMessage, r jobs.Reporter) (any, error) {
		var n [2]int
		if err := json.Unmarshal(args, &n); err != nil {
			return nil, err
		}
		<-c.start
		write(r, 1, n[0], n[0]+n[1])
		if c.paused != nil {
			close(c.paused)
			<-c.more
		}
		write(r, n[0]+1, n[0]+n[1], n[0]+n[1])
		return n[0] + n[1], nil
	}})
	if err != nil {
		t.Fatal(err)
	}
}

// checkLines fails unless the log events of a stream numbered the lines one
// to n, each once, in order.
func checkLines(t *testing.T, ids []string, n int) {
	t.Helper()
	if len(ids) != n {
		t.Fatalf("got %d lines of %d", len(ids), n)
	}
	for i, id := range ids {
		if id != strconv.Itoa(i+1) {
			t.Fatalf("line %d came as %s", i+1, id)
		}
	}
}

// A client that gets stuck while the job it follows writes more than the
// stream's buffer holds still gets every line of the log, once, in order,
// and the finished job last, also when the job goes on writing after the
// client caught up with what it had.
func TestJobStreamCatchesUp(t *testing.T) {
	s := newStack(t, RateLimit{})
	ctx := context.Background()
	start, more, paused := make(chan struct{}), make(chan struct{}), make(chan struct{})
	chatty{start: start, more: more, paused: paused}.register(t, s, "apitest.chatty")
	const first, second = 300, 200
	job, err := s.jobs.Enqueue(ctx, "apitest.chatty", [2]int{first, second}, jobs.Options{})
	if err != nil {
		t.Fatal(err)
	}
	gate := make(chan struct{})
	var ids []string
	var last *jobs.Job
	out := func(event, id string, data any) error {
		switch event {
		case "log":
			if id == "5" {
				// The client gets stuck here while the job writes on.
				<-gate
			}
			ids = append(ids, id)
		case "job":
			last = data.(*jobs.Job)
		}
		return nil
	}
	followed := make(chan error, 1)
	go func() { followed <- s.server.followJob(ctx, job.ID, 0, out) }()
	close(start)
	<-paused
	// The client takes what it can, and the job writes the rest once it
	// has.
	close(gate)
	time.Sleep(50 * time.Millisecond)
	close(more)
	select {
	case err := <-followed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the stream didn't end")
	}
	checkLines(t, ids, first+second)
	if last == nil || last.State != jobs.StateSucceeded || last.Progress.Done != first+second {
		t.Errorf("the last job event = %+v", last)
	}
}

// Over HTTP too, with a client that reads as the job writes.
func TestJobStreamOverHTTPMissesNothing(t *testing.T) {
	s := newStack(t, RateLimit{})
	ctx := context.Background()
	start := make(chan struct{})
	chatty{start: start}.register(t, s, "apitest.chatty")
	const n = 400
	job, err := s.jobs.Enqueue(ctx, "apitest.chatty", [2]int{n, 0}, jobs.Options{Actor: s.actors["owner"]})
	if err != nil {
		t.Fatal(err)
	}
	stream := s.open("owner", "/api/v1/jobs/"+string(job.ID)+"/stream")
	close(start)
	var ids []string
	var last jobs.Job
	readEvents(t, stream, func(event, id, data string) bool {
		switch event {
		case "log":
			ids = append(ids, id)
		case "job":
			last = jobs.Job{}
			json.Unmarshal([]byte(data), &last)
		}
		return last.State.Done()
	})
	checkLines(t, ids, n)
	if last.State != jobs.StateSucceeded {
		t.Errorf("the stream ends with the finished job: %+v", last)
	}
}
