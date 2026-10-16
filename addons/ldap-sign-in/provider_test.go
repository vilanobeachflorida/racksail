package main

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/vilanobeachflorida/racksail/addons/ldap-sign-in/ldap/ldaptest"
)

var directory = []ldaptest.Entry{
	{DN: "cn=reader,dc=example,dc=com", Password: "reader-secret", Attributes: map[string][]string{"objectClass": {"organizationalRole"}}},
	{DN: "uid=ada,ou=people,dc=example,dc=com", Password: "correct horse", Attributes: map[string][]string{
		"objectClass": {"person"}, "entryUUID": {"8e5f1ba4-7c35-4a1e-9b6a-1f4c3b1d2e70"}, "uid": {"ada"}, "cn": {"Ada Lovelace"},
		"mail":     {"ada@example.com"},
		"memberOf": {"cn=staff,ou=groups,dc=example,dc=com", "cn=engines,ou=groups,dc=example,dc=com"},
	}},
	{DN: "uid=alan,ou=people,dc=example,dc=com", Password: "enigma", Attributes: map[string][]string{
		"objectClass": {"person"}, "uid": {"alan"}, "cn": {"Alan Turing"},
	}},
}

// call calls a method the way the panel does, and returns the status and
// what the add-on answered.
func call(t *testing.T, method string, settings, input any) (int, map[string]any) {
	t.Helper()
	a := addon(slog.New(slog.DiscardHandler))
	body, err := json.Marshal(map[string]any{"input": map[string]any{"settings": settings, "input": input}})
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/provide/auth-provider/"+method, bytes.NewReader(body)))
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("%s answered %q", method, rec.Body.String())
	}
	return rec.Code, out
}

func settingsFor(s *ldaptest.Server, more map[string]any) map[string]any {
	out := map[string]any{
		"url": s.URL, "startTLS": true, "caCertificate": string(s.CA),
		"bindDN": "cn=reader,dc=example,dc=com", "secret": "reader-secret", "baseDN": "ou=people,dc=example,dc=com",
	}
	for k, v := range more {
		if v == nil {
			delete(out, k)
		} else {
			out[k] = v
		}
	}
	return out
}

func check(username, password string) map[string]string {
	return map[string]string{"username": username, "password": password}
}

func TestDescribe(t *testing.T) {
	s := ldaptest.New(t)
	code, out := call(t, "describe", settingsFor(s, nil), map[string]any{})
	if code != http.StatusOK || out["method"] != "password" {
		t.Errorf("describe: %d %v", code, out)
	}
	if code, out := call(t, "describe", map[string]any{}, nil); code != http.StatusUnprocessableEntity {
		t.Errorf("describing without settings: %d %v", code, out)
	}
}

func TestSignInWithABindDN(t *testing.T) {
	s := ldaptest.New(t, directory...)
	s.RequireTLS = true
	settings := settingsFor(s, map[string]any{"requiredGroups": []string{"staff"}})
	code, out := call(t, "check-password", settings, check("ada", "correct horse"))
	if code != http.StatusOK {
		t.Fatalf("%d %v", code, out)
	}
	want := map[string]any{
		"subject": "8e5f1ba4-7c35-4a1e-9b6a-1f4c3b1d2e70", "username": "ada", "name": "Ada Lovelace", "email": "ada@example.com",
		"groups": []any{"cn=staff,ou=groups,dc=example,dc=com", "cn=engines,ou=groups,dc=example,dc=com"},
	}
	if !reflect.DeepEqual(out["identity"], want) {
		t.Errorf("identity %v, want %v", out["identity"], want)
	}
	if binds := s.Binds(); !slices.Equal(binds, []string{"cn=reader,dc=example,dc=com", "uid=ada,ou=people,dc=example,dc=com"}) {
		t.Errorf("binds %q", binds)
	}
	if filters := s.Filters(); !slices.Equal(filters, []string{"(uid=ada)"}) {
		t.Errorf("filters %q", filters)
	}

	for name, c := range map[string]map[string]string{
		"a wrong password":  check("ada", "wrong"),
		"an unknown person": check("grace", "x"),
		"a wildcard":        check("*", "correct horse"),
		"no password":       check("ada", ""),
		"no username":       check("  ", "x"),
	} {
		code, out := call(t, "check-password", settings, c)
		if code != http.StatusOK || out["refused"] != true || out["identity"] != nil {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	if filters := s.Filters(); !slices.Contains(filters, `(uid=\2a)`) {
		t.Errorf("the wildcard wasn't escaped: %q", filters)
	}
	for _, b := range s.Binds() {
		if b == "" {
			t.Error("an empty bind reached the directory")
		}
	}

	code, out = call(t, "check-password", settings, check("alan", "enigma"))
	if code != http.StatusOK || out["refused"] != true || !strings.Contains(out["reason"].(string), "group") {
		t.Errorf("someone outside the required groups: %d %v", code, out)
	}
	settings["requiredGroups"] = []string{"cn=Engines, ou=groups, dc=example, dc=com"}
	if code, out := call(t, "check-password", settings, check("ada", "correct horse")); code != http.StatusOK || out["identity"] == nil {
		t.Errorf("a required group by its DN: %d %v", code, out)
	}
}

func TestSignInWithoutASearch(t *testing.T) {
	s := ldaptest.NewTLS(t, directory...)
	settings := map[string]any{"url": s.URL, "caCertificate": string(s.CA), "userDN": "uid={username},ou=people,dc=example,dc=com"}
	code, out := call(t, "check-password", settings, check("alan", "enigma"))
	if code != http.StatusOK {
		t.Fatalf("%d %v", code, out)
	}
	id := out["identity"].(map[string]any)
	if id["subject"] != "dn:uid=alan,ou=people,dc=example,dc=com" || id["username"] != "alan" || id["name"] != "Alan Turing" {
		t.Errorf("identity %v", id)
	}
	if code, out := call(t, "check-password", settings, check("alan,ou=people", "enigma")); code != http.StatusOK || out["refused"] != true {
		t.Errorf("a username that would change the DN: %d %v", code, out)
	}
	if binds := s.Binds(); !slices.Contains(binds, `uid=alan\,ou\=people,ou=people,dc=example,dc=com`) {
		t.Errorf("the username wasn't escaped in the DN: %q", binds)
	}
}

func TestDirectoryFailures(t *testing.T) {
	s := ldaptest.New(t, directory...)
	code, out := call(t, "check-password", settingsFor(s, map[string]any{"secret": "stale"}), check("ada", "correct horse"))
	if code != http.StatusBadGateway || !strings.Contains(out["detail"].(string), "bindDN") {
		t.Errorf("a wrong bind password: %d %v", code, out)
	}
	code, out = call(t, "check-password", settingsFor(s, map[string]any{"userFilter": "(|(uid={username})(objectClass=person))"}), check("ada", "correct horse"))
	if code != http.StatusBadGateway || !strings.Contains(out["detail"].(string), "more than one") {
		t.Errorf("a filter that matches several people: %d %v", code, out)
	}

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	code, out = call(t, "check-password", map[string]any{"url": "ldap://" + addr, "baseDN": "dc=example,dc=com", "timeoutSeconds": 1}, check("ada", "x"))
	if code != http.StatusBadGateway || !strings.Contains(out["detail"].(string), "can't be reached") {
		t.Errorf("a directory that isn't there: %d %v", code, out)
	}
}

func TestSettingsAreChecked(t *testing.T) {
	s := ldaptest.New(t)
	for want, settings := range map[string]map[string]any{
		"the directory's address":   settingsFor(s, map[string]any{"url": nil}),
		"isn't an ldap://":          settingsFor(s, map[string]any{"url": "https://ldap.example.com"}),
		"nothing after it":          settingsFor(s, map[string]any{"url": "ldap://ldap.example.com/dc=example"}),
		"encrypted from the start":  settingsFor(s, map[string]any{"url": "ldaps://ldap.example.com"}),
		"in the clear":              settingsFor(s, map[string]any{"url": "ldap://ldap.example.com", "startTLS": false}),
		"needs its password":        settingsFor(s, map[string]any{"secret": nil}),
		"userDN needs {username}":   settingsFor(s, map[string]any{"userDN": "uid=ada,dc=example,dc=com"}),
		"baseDN is where":           settingsFor(s, map[string]any{"baseDN": nil}),
		"userFilter needs":          settingsFor(s, map[string]any{"userFilter": "(uid=ada)"}),
		"userFilter: ldap":          settingsFor(s, map[string]any{"userFilter": "(uid={username}"}),
		"scope is sub":              settingsFor(s, map[string]any{"scope": "base"}),
		"timeoutSeconds":            settingsFor(s, map[string]any{"timeoutSeconds": 61}),
		"isn't an attribute's name": settingsFor(s, map[string]any{"emailAttribute": "mail)(uid=*"}),
		"no PEM certificate":        settingsFor(s, map[string]any{"caCertificate": "not a certificate"}),
		"can't be read":             settingsFor(s, map[string]any{"bindPassword": "reader-secret"}),
	} {
		code, out := call(t, "check-password", settings, check("ada", "x"))
		detail, _ := out["detail"].(string)
		if code != http.StatusUnprocessableEntity || !strings.Contains(detail, want) {
			t.Errorf("%s: %d %v", want, code, out)
		}
	}
	if len(s.Binds()) != 0 {
		t.Error("settings that can't work reached the directory")
	}
	local := map[string]any{"url": "ldap://127.0.0.1:389", "baseDN": "dc=example,dc=com"}
	if _, err := load(mustJSON(t, local)); err != nil {
		t.Errorf("ldap:// on the server itself: %v", err)
	}
}

func TestSubjects(t *testing.T) {
	s := ldaptest.New(t, ldaptest.Entry{DN: "CN=Grace Hopper,OU=People,DC=example,DC=com", Password: "cobol", Attributes: map[string][]string{
		"objectClass": {"person"}, "sAMAccountName": {"grace"}, "objectGUID": {"\x8f\x01\x00\xff\x10 binary"}, "displayName": {"Grace Hopper"},
	}}, directory[0])
	settings := settingsFor(s, map[string]any{
		"startTLS": false, "url": s.URL, "baseDN": "OU=People,DC=example,DC=com", "userFilter": "(sAMAccountName={username})",
		"usernameAttribute": "sAMAccountName", "subjectAttribute": "objectGUID", "nameAttribute": "displayName",
	})
	code, out := call(t, "check-password", settings, check("grace", "cobol"))
	if code != http.StatusOK {
		t.Fatalf("%d %v", code, out)
	}
	id := out["identity"].(map[string]any)
	if id["subject"] != "8f0100ff102062696e617279" || id["username"] != "grace" || id["name"] != "Grace Hopper" {
		t.Errorf("identity %v", id)
	}
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
