package packaging

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// stanzas reads a deb822 file: paragraphs of "Field: value" lines, with
// comments left out.
func stanzas(t *testing.T, text string) []map[string]string {
	t.Helper()
	var out []map[string]string
	cur := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "#"):
		case strings.TrimSpace(line) == "":
			if len(cur) > 0 {
				out = append(out, cur)
				cur = map[string]string{}
			}
		default:
			field, value, ok := strings.Cut(line, ":")
			if !ok || strings.ContainsAny(field, " \t") {
				t.Fatalf("not a deb822 field: %q", line)
			}
			if _, dup := cur[field]; dup {
				t.Fatalf("%s appears twice in a stanza", field)
			}
			cur[field] = strings.TrimSpace(value)
		}
	}
	if len(cur) > 0 {
		out = append(out, cur)
	}
	return out
}

var fillIn = regexp.MustCompile(`@[A-Z_]+@`)

func render(t *testing.T, name string, values map[string]string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("apt", name))
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for k, v := range values {
		text = strings.ReplaceAll(text, "@"+k+"@", v)
	}
	if left := fillIn.FindString(text); left != "" {
		t.Fatalf("%s still has %s after rendering", name, left)
	}
	return text
}

func TestAptPreferences(t *testing.T) {
	cases := map[string]map[string]string{
		"racksail.pref.in":       {"HOST": "apt.example.com"},
		"racksail-hold.pref.in":  {"VERSION": "1.4.*"},
		"debian-release.pref.in": {"CODENAME": "trixie"},
	}
	for name, values := range cases {
		prefs := stanzas(t, render(t, name, values))
		if len(prefs) == 0 {
			t.Errorf("%s has no entries", name)
		}
		for _, p := range prefs {
			if p["Package"] == "" || p["Pin"] == "" {
				t.Errorf("%s: an entry needs Package and Pin: %v", name, p)
			}
			if prio, err := strconv.Atoi(p["Pin-Priority"]); err != nil || prio == 0 {
				t.Errorf("%s: Pin-Priority %q must be a non-zero number", name, p["Pin-Priority"])
			}
		}
	}
	// The Debian pin lets the release in and keeps every other release out.
	prefs := stanzas(t, render(t, "debian-release.pref.in", map[string]string{"CODENAME": "trixie"}))
	last := prefs[len(prefs)-1]
	if prefs[0]["Pin"] != "release o=Debian,n=trixie" || prefs[0]["Pin-Priority"] != "990" || last["Pin"] != "release o=Debian" || last["Pin-Priority"] != "-1" {
		t.Errorf("Debian pins = %v", prefs)
	}
}

func TestAptSources(t *testing.T) {
	src := stanzas(t, render(t, "racksail.sources.in", map[string]string{"URI": "https://apt.example.com/racksail", "SUITE": "stable", "COMPONENT": "main"}))
	if len(src) != 1 {
		t.Fatalf("sources = %v", src)
	}
	for _, field := range []string{"Types", "URIs", "Suites", "Components", "Signed-By"} {
		if src[0][field] == "" {
			t.Errorf("the source lacks %s", field)
		}
	}
}

// The cloud-init examples are valid cloud configs that run the install
// script with options it has.
func TestCloudInitExamples(t *testing.T) {
	help, err := exec.Command("sh", "install.sh", "--help").CombinedOutput()
	if err != nil {
		t.Fatalf("install.sh --help: %v\n%s", err, help)
	}
	files, _ := filepath.Glob("cloud-init/user-data*.yaml")
	if len(files) == 0 {
		t.Fatal("no examples")
	}
	host := regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(data), "#cloud-config\n") {
			t.Errorf("%s must start with #cloud-config", f)
		}
		var cfg struct {
			Runcmd [][]string `yaml:"runcmd"`
		}
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		ran := false
		for _, cmd := range cfg.Runcmd {
			if len(cmd) < 2 || cmd[0] != "sh" || !strings.HasSuffix(cmd[1], "racksail-install.sh") {
				continue
			}
			ran = true
			for i, arg := range cmd[2:] {
				if strings.HasPrefix(arg, "--") && !strings.Contains(string(help), "  "+arg) {
					t.Errorf("%s passes %s, which install.sh doesn't have", f, arg)
				}
				if arg == "--hostname" && !host.MatchString(cmd[2+i+1]) {
					t.Errorf("%s: %s isn't a hostname", f, cmd[2+i+1])
				}
			}
			if !strings.Contains(strings.Join(cmd, " "), "--yes") {
				t.Errorf("%s: cloud-init has no terminal, so the script needs --yes", f)
			}
		}
		if !ran {
			t.Errorf("%s doesn't run the install script", f)
		}
	}
	meta, err := os.ReadFile("cloud-init/meta-data.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var md map[string]string
	if err := yaml.Unmarshal(meta, &md); err != nil || md["instance-id"] == "" {
		t.Errorf("meta-data = %v, %v", md, err)
	}
}
