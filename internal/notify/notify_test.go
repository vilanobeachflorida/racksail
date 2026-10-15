package notify

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/events"
)

type recorder struct {
	name string
	fail bool
	got  []Message
}

func (r *recorder) Type() string { return r.name }

func (r *recorder) Send(ctx context.Context, msg Message) error {
	if r.fail {
		return errors.New("connection refused")
	}
	r.got = append(r.got, msg)
	return nil
}

func TestRegistry(t *testing.T) {
	RegisterType(Type{Name: "test-recorder", Title: "Recorder", New: func(settings json.RawMessage, s Secrets) (Channel, error) {
		return &recorder{name: "test-recorder"}, nil
	}})
	RegisterType(Type{Name: "Bad Name"})
	if Err() == nil {
		t.Error("a malformed type should be reported")
	}
	c, err := New("test-recorder", nil, nil)
	if err != nil || c.Type() != "test-recorder" {
		t.Errorf("New = %v, %v", c, err)
	}
	if _, err := New("pigeon", nil, nil); !errors.Is(err, ErrUnknownType) {
		t.Errorf("unknown type: %v", err)
	}
	found := false
	for _, typ := range Types() {
		found = found || typ.Name == "test-recorder"
	}
	if !found {
		t.Error("Types should list the registered type")
	}
}

func TestNotifierKeepsGoing(t *testing.T) {
	ok, broken := &recorder{name: "ok"}, &recorder{name: "broken", fail: true}
	n := &Notifier{Channels: []Channel{broken, ok}}
	err := n.Send(context.Background(), Message{Title: "Backup failed", Body: "The nightly backup failed.", Severity: events.SeverityError})
	if !errors.Is(err, ErrSendFailed) {
		t.Errorf("err = %v", err)
	}
	if len(ok.got) != 1 {
		t.Error("a failing channel must not stop the others")
	}
}
