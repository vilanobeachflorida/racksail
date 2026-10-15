package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/danielgtaylor/huma/v2"
)

// heartbeat is how often a quiet stream sends a comment, so proxies keep
// the connection open.
const heartbeat = 20 * time.Second

// send writes one Server-Sent Event: its name, its ID (which a client sends
// back as Last-Event-ID when it reconnects; empty for none) and its data as
// JSON.
type send func(event, id string, data any) error

// streamer writes Server-Sent Events to a response.
type streamer struct {
	mu sync.Mutex
	w  io.Writer
	rc *http.ResponseController
}

func newStreamer(w io.Writer) *streamer {
	s := &streamer{w: w}
	if rw, ok := w.(http.ResponseWriter); ok {
		s.rc = http.NewResponseController(rw)
	}
	return s
}

func (s *streamer) flush() error {
	if s.rc != nil {
		return s.rc.Flush()
	}
	if f, ok := s.w.(http.Flusher); ok {
		f.Flush()
	}
	return nil
}

func (s *streamer) send(event, id string, data any) error {
	raw, err := json.Marshal(data)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var b strings.Builder
	if id != "" {
		fmt.Fprintf(&b, "id: %s\n", id)
	}
	fmt.Fprintf(&b, "event: %s\ndata: %s\n\n", event, raw)
	if _, err := io.WriteString(s.w, b.String()); err != nil {
		return err
	}
	return s.flush()
}

func (s *streamer) comment(text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := io.WriteString(s.w, ": "+text+"\n\n"); err != nil {
		return err
	}
	return s.flush()
}

// streamResponse documents a stream of Server-Sent Events whose events are
// named after the keys of messages and carry their values' types as JSON.
func (s *Server) streamResponse(messages map[string]any) *huma.Response {
	names := make([]string, 0, len(messages))
	for name := range messages {
		names = append(names, name)
	}
	sort.Strings(names)
	var variants []*huma.Schema
	for _, name := range names {
		t := reflect.TypeOf(messages[name])
		for t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		variants = append(variants, &huma.Schema{
			Title: "Event " + name,
			Type:  huma.TypeObject,
			Properties: map[string]*huma.Schema{
				"event": {Type: huma.TypeString, Description: "The event's name", Extensions: map[string]any{"const": name}},
				"id":    {Type: huma.TypeString, Description: "Send it back as Last-Event-ID to resume after this event"},
				"data":  s.schemaOf(t, name),
			},
			Required: []string{"event", "data"},
		})
	}
	return &huma.Response{
		Description: "A stream of Server-Sent Events, one JSON document per event",
		Content: map[string]*huma.MediaType{"text/event-stream": {Schema: &huma.Schema{
			Title:       "Server-Sent Events",
			Description: "Each item is one possible event of the stream.",
			Type:        huma.TypeArray,
			Items:       &huma.Schema{Extensions: map[string]any{"oneOf": variants}},
		}}},
	}
}

// stream runs a streaming operation: open checks the request, and what it
// returns runs once the stream has started, until the client goes away or
// it returns.
type stream func(ctx context.Context, out send) error

// registerStream registers an operation that answers with Server-Sent
// Events. open runs before anything is sent, so its errors are problems
// like any other operation's.
func registerStream[I any](s *Server, op huma.Operation, messages map[string]any, open func(ctx context.Context, in *I) (stream, error)) {
	if op.Responses == nil {
		op.Responses = map[string]*huma.Response{}
	}
	op.Responses["200"] = s.streamResponse(messages)
	s.withProblems(&op)
	huma.Register(s.api, op, func(ctx context.Context, in *I) (*huma.StreamResponse, error) {
		run, err := open(ctx, in)
		if err != nil {
			return nil, err
		}
		return &huma.StreamResponse{Body: func(hc huma.Context) {
			hc.SetHeader("Content-Type", "text/event-stream")
			hc.SetHeader("Cache-Control", "no-store")
			hc.SetHeader("X-Accel-Buffering", "no")
			hc.SetStatus(http.StatusOK)
			out := newStreamer(hc.BodyWriter())
			if err := out.comment("connected"); err != nil {
				return
			}
			ctx, cancel := context.WithCancel(hc.Context())
			defer cancel()
			go func() {
				ticker := time.NewTicker(heartbeat)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return
					case <-ticker.C:
						if out.comment("keep-alive") != nil {
							cancel()
							return
						}
					}
				}
			}()
			if err := run(ctx, out.send); err != nil && ctx.Err() == nil {
				s.opts.Log.Debug("a stream ended early", "operation", op.OperationID, "error", err)
			}
		}}, nil
	})
}
