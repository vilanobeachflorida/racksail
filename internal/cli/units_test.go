package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestNames(t *testing.T) {
	for in, want := range map[string]string{
		"memoryMax": "memory-max", "signin_policy": "signin-policy", "listMySSHKeys": "list-my-ssh-keys",
		"allowFrom": "allow-from", "Idempotency-Key": "idempotency-key", "url": "url", "cpuWeight": "cpu-weight",
	} {
		if got := kebab(in); got != want {
			t.Errorf("kebab(%q) = %q, want %q", in, got, want)
		}
	}
	for in, want := range map[string]string{
		"Account": "Accounts", "Sign-in policy": "Sign-in policies", "Mailbox": "Mailboxes", "Alias": "Aliases",
		"Day": "Days", "Match": "Matches", "DNS zone": "DNS zones",
	} {
		if got := pluralTitle(in); got != want {
			t.Errorf("pluralTitle(%q) = %q, want %q", in, got, want)
		}
	}
	if lowerFirst("Sign-in policies") != "sign-in policies" || lowerFirst("DNS zones") != "DNS zones" {
		t.Error("lowerFirst")
	}
	for _, tc := range []struct {
		op   Operation
		want string
	}{
		{Operation{ID: "listCoreAccounts", Kind: "core.account", Operation: "list", Path: "/api/v1/core/accounts"}, "account list"},
		{Operation{ID: "restartWebSite", Kind: "web.site", Operation: "restart_php", Path: "/api/v1/web/sites/{id}/actions/restart_php"}, "web site restart-php"},
		{Operation{ID: "streamJob", Path: "/api/v1/jobs/{id}/stream"}, "job watch"},
		{Operation{ID: "purgeMailQueue", Path: "/api/v1/mail/queue/purge"}, "mail purge-mail-queue"},
	} {
		if got := strings.Join(CommandPath(&tc.op), " "); got != tc.want {
			t.Errorf("%s = %q, want %q", tc.op.ID, got, tc.want)
		}
	}
}

func TestSizes(t *testing.T) {
	for in, want := range map[string]int64{
		"512": 512, "1K": 1024, "2G": 2 << 30, "2GiB": 2 << 30, "1.5GB": 1_500_000_000, "512m": 512 << 20, "1T": 1 << 40, "3 MB": 3_000_000,
	} {
		got, err := parseSize(in)
		if err != nil || got != want {
			t.Errorf("parseSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
	for _, in := range []string{"", "two", "1X", "-1", "1..2G"} {
		if _, err := parseSize(in); err == nil {
			t.Errorf("parseSize(%q) worked", in)
		}
	}
	if humanBytes(512) != "512 B" || humanBytes(2048) != "2 KiB" || humanBytes(1536<<20) != "1.5 GiB" {
		t.Errorf("humanBytes = %s, %s, %s", humanBytes(512), humanBytes(2048), humanBytes(1536<<20))
	}
}

func TestOwnersAndPaths(t *testing.T) {
	for in, want := range map[string]string{
		"server": `{"level":"server"}`, "acct_1": `{"id":"acct_1","level":"account"}`,
		"reseller:user_9": `{"id":"user_9","level":"reseller"}`, "account:acct_2": `{"id":"acct_2","level":"account"}`,
	} {
		got, err := parseScope("owner", in)
		if err != nil || jsonOf(got) != want {
			t.Errorf("parseScope(%q) = %s, %v", in, jsonOf(got), err)
		}
	}
	for _, in := range []string{"", "everyone", "account:", "user_1"} {
		if _, err := parseScope("owner", in); err == nil {
			t.Errorf("parseScope(%q) worked", in)
		}
	}

	doc := map[string]any{"spec": map[string]any{"name": "x"}}
	for path, value := range map[string]any{"spec.domains[1].name": "b.example", "spec.limits.cpu": 200, "labels.team": "ops"} {
		steps, err := parsePath(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := setAt(doc, steps, value); err != nil {
			t.Fatal(err)
		}
	}
	if got := jsonOf(doc); got != `{"labels":{"team":"ops"},"spec":{"domains":[null,{"name":"b.example"}],"limits":{"cpu":200},"name":"x"}}` {
		t.Errorf("doc = %s", got)
	}
	for _, bad := range []string{"", "a..b", "a[x]", "a]", "[0]"} {
		if _, err := parsePath(bad); err == nil {
			t.Errorf("parsePath(%q) worked", bad)
		}
	}
	steps, _ := parsePath("spec.name.first")
	if _, err := setAt(doc, steps, "x"); err == nil {
		t.Error("set a field inside a string")
	}
	if v := looseJSON(`{"a":1}`); jsonOf(v) != `{"a":1}` {
		t.Errorf("looseJSON object = %v", v)
	}
	if v := looseJSON("plain text"); v != "plain text" {
		t.Errorf("looseJSON text = %v", v)
	}
}

func TestServers(t *testing.T) {
	for in, want := range map[string]string{
		"panel.example.com": "https://panel.example.com", "127.0.0.1:7070": "http://127.0.0.1:7070",
		"localhost:7213": "http://localhost:7213", "https://panel.example.com/": "https://panel.example.com",
		"http://[::1]:7070": "http://[::1]:7070",
	} {
		got, err := NormalizeServer(in)
		if err != nil || got != want {
			t.Errorf("NormalizeServer(%q) = %q, %v", in, got, err)
		}
	}
	for _, in := range []string{"", "ftp://x", "https://user:rsk_x@panel.example.com"} {
		if _, err := NormalizeServer(in); err == nil {
			t.Errorf("NormalizeServer(%q) worked", in)
		}
	}
	if ProfileName("https://panel.example.com") != "panel.example.com" || ProfileName("http://127.0.0.1:7213") != "127.0.0.1-7213" {
		t.Error("ProfileName")
	}
}

func TestReadingEvents(t *testing.T) {
	stream := ": hello\n\nid: 3\nevent: log\ndata: {\"a\":\ndata: 1}\n\nevent: job\ndata: {}\n\ndata: lone\n\n"
	var got []string
	err := readEvents(strings.NewReader(stream), func(ev sseEvent) error {
		got = append(got, ev.ID+"|"+ev.Event+"|"+string(ev.Data))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "3|log|{\"a\":\n1}  |job|{}  |message|lone"; strings.Join(got, "  ") != want {
		t.Errorf("events = %q", strings.Join(got, "  "))
	}
}

func TestTables(t *testing.T) {
	v, err := decodeOrdered([]byte(`{"users":[{"id":"user_1","home":{"level":"account","id":"acct_1"},"username":"ada","createdAt":"2026-10-15T10:00:00Z",
		"grants":[{"role":"owner"}],"disabled":false,"bio":""},{"id":"user_2","home":{"level":"server"},"username":"bob","createdAt":"2026-10-15T10:00:00Z","disabled":true}],"next":"c2"}`))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	printTable(&out, v, nil)
	lines := strings.Split(out.String(), "\n")
	if !strings.HasPrefix(lines[0], "ID") || !strings.Contains(lines[0], "USERNAME") || strings.Contains(lines[0], "GRANTS") || strings.Contains(lines[0], "BIO") {
		t.Errorf("header = %q", lines[0])
	}
	if !strings.Contains(lines[1], "account:acct_1") || !strings.Contains(lines[2], "yes") || !strings.Contains(out.String(), "--cursor c2") {
		t.Errorf("table =\n%s", out.String())
	}

	v, _ = decodeOrdered([]byte(`{"id":"x","spec":{"name":"n","limits":{"memoryMax":5}},"progress":{"done":3,"total":7},"items":[{"id":"a"},{"id":"b"}]}`))
	out.Reset()
	printTable(&out, v, nil)
	for _, s := range []string{"spec.name:", "spec.limits.memoryMax:", "progress:", "3/7", "ITEMS:"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("detail lacks %q:\n%s", s, out.String())
		}
	}
	// Lists of objects further in are counted.
	v, _ = decodeOrdered([]byte(`{"id":"user_1","owner":{"grants":[{"role":"owner"}],"keys":[{"id":"k1"},{"id":"k2"}]}}`))
	out.Reset()
	printTable(&out, v, nil)
	for _, s := range []string{"owner.grants:  1 item\n", "owner.keys:    2 items\n"} {
		if !strings.Contains(out.String(), s) {
			t.Errorf("detail lacks %q:\n%s", s, out.String())
		}
	}

	if got := candidates([]byte(`{"items":[{"id":"whk_1","spec":{"url":"https://a"}},{"id":"site_1","name":"blog"}]}`), "id", "whk"); strings.Join(got, ",") != "whk_1\thttps://a" {
		t.Errorf("candidates = %q", got)
	}
	if got := candidates([]byte(`{"kinds":[{"name":"core.webhook","title":"Webhook"}]}`), "name", ""); strings.Join(got, ",") != "core.webhook\tWebhook" {
		t.Errorf("kind candidates = %q", got)
	}
}
