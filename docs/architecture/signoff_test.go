package architecture

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// The documents that tell people how to write commits, and the workflow
// that checks pull requests.
const (
	contributingGuide = "../../CONTRIBUTING.md"
	contributingPage  = "../site/src/content/docs/community/contributing.md"
	signOffWorkflow   = "../../.github/workflows/sign-off.yml"
)

var listItem = regexp.MustCompile(`^\s*([-*]|[0-9]+\.)\s`)

// blocks splits Markdown into paragraphs and list items.
func blocks(md string) []string {
	var out []string
	var cur []string
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.Join(cur, " "))
			cur = nil
		}
	}
	for _, line := range strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n") {
		switch {
		case strings.TrimSpace(line) == "":
			flush()
		case listItem.MatchString(line):
			flush()
			cur = append(cur, strings.TrimSpace(line))
		default:
			cur = append(cur, strings.TrimSpace(line))
		}
	}
	flush()
	return out
}

// TestSignOffRuleIsTheSameEverywhere holds conventions.md, CONTRIBUTING.md
// and the site's contributing page to one rule: the commits of a pull
// request are signed off by their authors, the maintainers' own commits to
// main aren't, and no commit carries any other trailer. A document that
// speaks of trailers anywhere else, as "No trailers" once did while the
// workflow asked for sign-offs, contradicts it.
func TestSignOffRuleIsTheSameEverywhere(t *testing.T) {
	for _, path := range []string{"conventions.md", contributingGuide, contributingPage} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var rule []string
		for _, b := range blocks(string(data)) {
			lower := strings.ToLower(b)
			signOff := strings.Contains(b, "Signed-off-by") || strings.Contains(lower, "sign-off") || strings.Contains(lower, "sign off") ||
				strings.Contains(lower, "signed off")
			if signOff {
				rule = append(rule, b)
			}
			if strings.Contains(lower, "trailer") && !strings.Contains(b, "Signed-off-by") {
				t.Errorf("%s speaks of trailers without the Signed-off-by line pull requests need: %q", path, b)
			}
		}
		all := strings.Join(rule, "\n")
		for _, want := range []string{"Signed-off-by", "pull request", "maintainers", "`main`", "trailer", "Developer Certificate of Origin"} {
			if !strings.Contains(all, want) {
				t.Errorf("%s doesn't say what the sign-off rule says about %q", path, want)
			}
		}
	}
}

// workflow is the part of a GitHub Actions workflow the test reads.
type workflow struct {
	On   map[string]any `yaml:"on"`
	Jobs map[string]struct {
		Steps []struct {
			Name string            `yaml:"name"`
			Env  map[string]string `yaml:"env"`
			Run  string            `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

// signOffScript returns the script of the Sign-off workflow's check and the
// environment it is given.
func signOffScript(t *testing.T) (string, map[string]string, []string) {
	t.Helper()
	data, err := os.ReadFile(signOffWorkflow)
	if err != nil {
		t.Fatal(err)
	}
	var wf workflow
	if err := yaml.Unmarshal(data, &wf); err != nil {
		t.Fatal(err)
	}
	var triggers []string
	for on := range wf.On {
		triggers = append(triggers, on)
	}
	sort.Strings(triggers)
	for _, job := range wf.Jobs {
		for _, s := range job.Steps {
			if s.Name == "Check every commit's sign-off" {
				return s.Run, s.Env, triggers
			}
		}
	}
	t.Fatal("the Sign-off workflow has no step called \"Check every commit's sign-off\"")
	return "", nil, nil
}

// repo is a git repository of the test's own, which no configuration of
// the person running the tests reaches.
type repo struct {
	t   *testing.T
	dir string
	env []string
}

func newRepo(t *testing.T) *repo {
	t.Helper()
	dir := t.TempDir()
	r := &repo{t: t, dir: dir, env: []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + dir, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=" + filepath.Join(dir, "gitconfig"),
		"LC_ALL=C", "TZ=UTC",
	}}
	r.git(nil, "init", "--quiet")
	r.git(nil, "symbolic-ref", "HEAD", "refs/heads/main")
	return r
}

func (r *repo) git(env []string, args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "commit.gpgsign=false", "-c", "core.hooksPath=/dev/null"}, args...)...)
	cmd.Dir = r.dir
	cmd.Env = append(slices.Clone(r.env), env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// person is who writes a commit.
type person struct{ name, email string }

func (p person) String() string { return p.name + " <" + p.email + ">" }

var (
	maintainer = person{"Grace Hopper", "grace@example.com"}
	ada        = person{"Ada Lovelace", "ada@example.com"}
	bob        = person{"Bob Dobbs", "bob@example.com"}
	renovate   = person{"renovate[bot]", "29139614+renovate[bot]@users.noreply.github.com"}
)

// commit makes an empty commit by who, ending with signedOff's sign-off
// when there is one.
func (r *repo) commit(who person, subject string, signedOff *person) {
	r.t.Helper()
	args := []string{"commit", "--quiet", "--allow-empty", "-m", subject}
	if signedOff != nil {
		args = append(args, "-m", "Signed-off-by: "+signedOff.String())
	}
	r.git([]string{
		"GIT_AUTHOR_NAME=" + who.name, "GIT_AUTHOR_EMAIL=" + who.email,
		"GIT_COMMITTER_NAME=" + who.name, "GIT_COMMITTER_EMAIL=" + who.email,
	}, args...)
}

// check runs the workflow's script as GitHub runs it, for the pull request
// from head into base, and returns its output, its step summary and
// whether it passed.
func (r *repo) check(script, base, head string) (string, string, bool) {
	r.t.Helper()
	bash, err := exec.LookPath("bash")
	if err != nil {
		r.t.Skip("bash isn't installed")
	}
	file := filepath.Join(r.dir, ".check.sh")
	summary := filepath.Join(r.dir, ".summary")
	if err := os.WriteFile(file, []byte(script), 0o600); err != nil {
		r.t.Fatal(err)
	}
	cmd := exec.Command(bash, "--noprofile", "--norc", "-eo", "pipefail", file)
	cmd.Dir = r.dir
	cmd.Env = append(slices.Clone(r.env), "BASE_BRANCH="+base, "PR_HEAD="+r.git(nil, "rev-parse", head), "GITHUB_STEP_SUMMARY="+summary)
	out, err := cmd.CombinedOutput()
	if _, ok := err.(*exec.ExitError); err != nil && !ok {
		r.t.Fatal(err)
	}
	sum, _ := os.ReadFile(summary)
	return string(out), string(sum), err == nil
}

// TestSignOffWorkflowChecksWhatPullRequestsAdd runs the Sign-off
// workflow's script on a pull request of a repository of the test's own:
// the commits the pull request adds need their author's sign-off, and
// nothing else is checked, neither the maintainers' commits on main, before
// the branch or merged into it, nor merge commits and bots' commits.
func TestSignOffWorkflowChecksWhatPullRequestsAdd(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git isn't installed")
	}
	script, env, triggers := signOffScript(t)
	if !slices.Equal(triggers, []string{"pull_request"}) {
		t.Errorf("the Sign-off workflow runs on %v; it checks pull requests only, since the maintainers' own commits to main carry no sign-off", triggers)
	}
	if !strings.Contains(env["BASE_BRANCH"], "github.event.pull_request.base.ref") || !strings.Contains(env["PR_HEAD"], "github.event.pull_request.head.sha") {
		t.Errorf("the check isn't given the pull request's base and head: %v", env)
	}

	r := newRepo(t)
	r.commit(maintainer, "Add DNS zone import", nil)
	r.git(nil, "switch", "--quiet", "-c", "feature")
	r.commit(ada, "Fix a typo in the zone import", &ada)
	r.commit(bob, "Test the zone import", nil)
	r.commit(bob, "Name the zone import's errors", &ada)
	r.commit(renovate, "Update the Go modules", nil)
	r.git(nil, "switch", "--quiet", "main")
	r.commit(maintainer, "Describe the zone import", nil)
	r.git(nil, "switch", "--quiet", "feature")
	r.git([]string{"GIT_AUTHOR_NAME=" + bob.name, "GIT_AUTHOR_EMAIL=" + bob.email, "GIT_COMMITTER_NAME=" + bob.name, "GIT_COMMITTER_EMAIL=" + bob.email},
		"merge", "--quiet", "--no-edit", "main")

	out, summary, passed := r.check(script, "main", "feature")
	if passed {
		t.Fatalf("a pull request with commits that aren't signed off by their authors passed:\n%s", out)
	}
	for _, want := range []string{"Test the zone import", "Name the zone import's errors"} {
		if !strings.Contains(out, want) || !strings.Contains(summary, want) {
			t.Errorf("the check doesn't name %q, which its author didn't sign off:\n%s", want, out)
		}
	}
	for _, unchecked := range []string{"Fix a typo", "Add DNS zone import", "Describe the zone import", "Update the Go modules", "Merge branch"} {
		if strings.Contains(out, unchecked) {
			t.Errorf("the check names %q, which needs no sign-off:\n%s", unchecked, out)
		}
	}
	if base := r.git(nil, "merge-base", "main", "feature"); !strings.Contains(out, "git rebase --signoff "+base) {
		t.Errorf("the check doesn't say how to sign off what's missing:\n%s", out)
	}

	r.git(nil, "switch", "--quiet", "-c", "fixed", "main")
	r.commit(ada, "Fix a typo in the zone import", &ada)
	r.commit(bob, "Test the zone import", &bob)
	r.commit(renovate, "Update the Go modules", nil)
	if out, _, passed := r.check(script, "main", "fixed"); !passed || !strings.Contains(out, "Every commit is signed off") {
		t.Errorf("a pull request whose commits are all signed off failed:\n%s", out)
	}
}
