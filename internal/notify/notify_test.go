package notify

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/theme"
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
	RegisterType(Type{Name: "test-recorder", Title: "Recorder", New: func(settings json.RawMessage, env Env) (Channel, error) {
		return &recorder{name: "test-recorder"}, nil
	}})
	defer func() {
		types.Lock()
		delete(types.m, "test-recorder")
		types.errs = nil
		types.Unlock()
	}()
	RegisterType(Type{Name: "Bad Name"})
	if Err() == nil {
		t.Error("a malformed type should be reported")
	}
	c, err := New("test-recorder", nil, Env{})
	if err != nil || c.Type() != "test-recorder" {
		t.Errorf("New = %v, %v", c, err)
	}
	if _, err := New("pigeon", nil, Env{}); !errors.Is(err, ErrUnknownType) {
		t.Errorf("unknown type: %v", err)
	}
	var names []string
	for _, typ := range Types() {
		names = append(names, typ.Name)
	}
	for _, want := range []string{"discord", "email", "matrix", "ntfy", "slack", "telegram", "test-recorder", "webhook"} {
		if !strings.Contains(strings.Join(names, ","), want) {
			t.Errorf("Types should list %s: %v", want, names)
		}
	}
}

func TestBuiltInTypesRegisterCleanly(t *testing.T) {
	for _, typ := range Types() {
		if typ.Settings == nil || typ.Description == "" {
			t.Errorf("%s needs settings and a description", typ.Name)
		}
		for _, s := range typ.Secrets {
			found := false
			for _, f := range SecretFields {
				found = found || f == s
			}
			if !found {
				t.Errorf("%s keeps a secret in %s, which channels don't treat as sensitive", typ.Name, s)
			}
		}
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

func TestFormatting(t *testing.T) {
	msg := Message{
		Title: "Disk almost full", Body: "The data disk is 93% full.", Severity: events.SeverityWarning,
		URL: "https://panel.example.com/storage", ActionLabel: "Look at storage",
	}
	if got := Subject(msg); got != "Warning: Disk almost full" {
		t.Errorf("Subject = %q", got)
	}
	if got := Subject(Message{Title: "Site created", Severity: events.SeverityInfo}); got != "Site created" {
		t.Errorf("an informational subject carries no label: %q", got)
	}
	text := Text(msg)
	if !strings.Contains(text, "93% full") || !strings.HasSuffix(text, "Look at storage: https://panel.example.com/storage") {
		t.Errorf("Text = %q", text)
	}
	digest := Text(Message{Title: "2 notifications", Body: "From quiet hours.", Digest: []Message{
		{Title: "Backup failed", Severity: events.SeverityError, Body: "Nightly."},
		{Title: "Site created", Body: "shop.example.com"},
	}})
	if !strings.Contains(digest, "Error: Backup failed\nNightly.") || !strings.Contains(digest, "Site created\nshop.example.com") {
		t.Errorf("a digest lists its messages: %q", digest)
	}
	page, err := HTML(context.Background(), Message{Title: "<script>alert(1)</script>", Body: "a & b", URL: "javascript:alert(1)"}, Env{BrandName: "Acme Hosting"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(page, "<script>alert") || strings.Contains(page, "javascript:") {
		t.Error("the email template escapes what it is given")
	}
	if !strings.Contains(page, "Acme Hosting") || !strings.Contains(page, "a &amp; b") {
		t.Error("the email carries the brand and the body")
	}
	// A theme of the panel's own dresses the email.
	custom := &theme.Package{Theme: theme.HighContrast()}
	if _, err := HTML(context.Background(), msg, Env{Theme: func(context.Context) (*theme.Package, error) { return custom, nil }}); err != nil {
		t.Errorf("rendering with the panel's theme: %v", err)
	}
}
