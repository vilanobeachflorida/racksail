package api

import (
	"net/http"
	"testing"
)

// What a person chooses in the panel follows them to every browser they
// sign in on, how it looks and how it works for them, with the language of
// their profile.
func TestPreferencesFollowThePerson(t *testing.T) {
	s := newStack(t, RateLimit{})
	b := s.setUp()
	prefs := map[string]any{
		"mode": "dark", "density": "compact", "timeZone": "America/Argentina/Buenos_Aires", "hourCycle": "h23",
		"letterShortcuts": false, "rememberRecent": true,
	}
	if resp := b.do("PUT", "/api/v1/session/preferences", prefs, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("keeping preferences = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if resp := b.do("POST", "/api/v1/session/step-up", map[string]any{"password": testPassword}, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("step-up = %d %s", resp.StatusCode, bodyOf(resp))
	}
	if resp := b.do("PUT", "/api/v1/me", map[string]any{"name": "Ada", "locale": "pt-BR"}, nil); resp.StatusCode != http.StatusAccepted {
		t.Fatalf("changing the profile = %d %s", resp.StatusCode, bodyOf(resp))
	}

	other := s.browser()
	if resp := other.do("POST", "/api/v1/auth/sign-in", map[string]any{"username": "ada", "password": testPassword}, nil); resp.StatusCode != http.StatusOK {
		t.Fatalf("sign-in = %d %s", resp.StatusCode, bodyOf(resp))
	}
	var session Session
	other.do("GET", "/api/v1/session", nil, &session)
	p := session.Preferences
	if p == nil || p.Mode != "dark" || p.Density != "compact" || p.Contrast != "" || p.TimeZone != "America/Argentina/Buenos_Aires" ||
		p.HourCycle != "h23" || p.LetterShortcuts == nil || *p.LetterShortcuts || p.RememberRecent == nil || !*p.RememberRecent {
		t.Errorf("preferences in another browser = %+v", p)
	}
	if session.User.Locale != "pt-BR" {
		t.Errorf("language in another browser = %q", session.User.Locale)
	}

	// A new set replaces the one before, and what's left out goes back to
	// the UI's defaults.
	if resp := other.do("PUT", "/api/v1/session/preferences", map[string]any{"mode": "light"}, nil); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("keeping preferences = %d %s", resp.StatusCode, bodyOf(resp))
	}
	var after Session
	b.do("GET", "/api/v1/session", nil, &after)
	if p := after.Preferences; p == nil || p.Mode != "light" || p.Density != "" || p.TimeZone != "" || p.LetterShortcuts != nil {
		t.Errorf("preferences after another browser's change = %+v", p)
	}

	// Values the UI has no use for are refused, and nothing changes.
	for _, bad := range []map[string]any{
		{"hourCycle": "h24"}, {"timeZone": "Europe/Lisbon; rm"}, {"timeZone": "/etc/passwd"}, {"letterShortcuts": "no"}, {"colour": "red"},
	} {
		if resp := b.do("PUT", "/api/v1/session/preferences", bad, nil); resp.StatusCode != http.StatusUnprocessableEntity {
			t.Errorf("%v = %d %s", bad, resp.StatusCode, bodyOf(resp))
		}
	}
	var kept Session
	b.do("GET", "/api/v1/session", nil, &kept)
	if p := kept.Preferences; p == nil || p.Mode != "light" {
		t.Errorf("preferences after refused changes = %+v", p)
	}
}
