package identity

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/notify"
)

// Security alerts go out through the notification channel types the core
// has, built the way the notification service builds its channels. A
// person's secret settings are the values they gave: one that names a
// secret of the panel's reaches nothing.
func TestAlertsThroughTheCoreChannels(t *testing.T) {
	ctx := context.Background()
	type post struct {
		auth string
		body map[string]any
	}
	posts := make(chan post, 4)
	ntfy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		json.Unmarshal(raw, &body)
		posts <- post{auth: r.Header.Get("Authorization"), body: body}
	}))
	defer ntfy.Close()

	h := newHarness(t)
	bob := h.admin("bob")
	// What bob's authenticator app is checked with, which nobody else may
	// ever read.
	theirs := secretRef(bob.ID, totpSecret)
	if err := h.vault.Put(ctx, theirs, []byte("JBSWY3DPEHPK3PXP")); err != nil {
		t.Fatal(err)
	}
	settings, _ := json.Marshal(notify.NtfySettings{Server: ntfy.URL, Topic: "alerts", Token: string(theirs)})

	// Without the notification service's client, channels reach public
	// addresses only, which this server isn't.
	h.admin("ada")
	actor, _ := h.signIn("ada")
	if err := h.svc.SetAlertChannel(ctx, actor, AlertChannelInput{Type: "ntfy", Settings: settings}); err != nil {
		t.Fatal(err)
	}
	if err := h.svc.TestAlert(ctx, actor); err == nil {
		t.Error("a channel without the notification service's client reached a loopback address")
	}

	h.svc.opts.Channels = func() notify.Env { return notify.Env{HTTP: ntfy.Client(), BrandName: "Racksail"} }
	if err := h.svc.TestAlert(ctx, actor); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-posts:
		if p.auth != "Bearer "+string(theirs) {
			t.Errorf("the alert signed in to ntfy with %q, not the token ada gave", p.auth)
		}
		if strings.Contains(p.auth, "JBSWY3DPEHPK3PXP") {
			t.Error("ada's alert settings reached bob's authenticator secret")
		}
		if p.body["topic"] != "alerts" || !strings.Contains(p.body["title"].(string), "Security alerts work") {
			t.Errorf("posted %v", p.body)
		}
	default:
		t.Fatal("the test alert wasn't posted")
	}

	// A sign-in from a device ada hasn't used goes out the same way.
	elsewhere := auth.Client{IP: "203.0.113.200", UserAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) Version/18.0 Mobile/15E148 Safari/604.1"}
	if _, err := h.svc.SignIn(ctx, SignInInput{Username: "ada", Password: password}, "", elsewhere); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-posts:
		title, _ := p.body["title"].(string)
		message, _ := p.body["message"].(string)
		if !strings.Contains(title, "New sign-in to your account") || !strings.Contains(message, "203.0.113.200") {
			t.Errorf("the sign-in alert posted %v", p.body)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the sign-in alert wasn't posted")
	}
}
