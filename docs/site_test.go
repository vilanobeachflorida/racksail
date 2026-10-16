package docs

import (
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"unicode"

	"go.yaml.in/yaml/v3"

	"github.com/vilanobeachflorida/racksail/internal/modules"
	_ "github.com/vilanobeachflorida/racksail/modules/all"
)

// sitePage is a page of the documentation site.
type sitePage struct {
	slug, file  string
	title, desc string
	// splash is a landing page, whose words are mostly in its frontmatter.
	splash bool
	body   string
}

// pages reads every page of the site, by its slug.
func pages(t *testing.T) map[string]sitePage {
	t.Helper()
	out := map[string]sitePage{}
	err := filepath.WalkDir(contentDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && strings.HasPrefix(d.Name(), ".") {
			return filepath.SkipDir
		}
		ext := filepath.Ext(path)
		if d.IsDir() || (ext != ".md" && ext != ".mdx") {
			return nil
		}
		rel, _ := filepath.Rel(contentDir, path)
		slug := strings.TrimSuffix(filepath.ToSlash(rel), ext)
		text := read(t, path)
		front, body, ok := strings.Cut(strings.TrimPrefix(text, "---\n"), "\n---\n")
		if !strings.HasPrefix(text, "---\n") || !ok {
			t.Errorf("%s has no frontmatter", path)
			return nil
		}
		var meta struct {
			Title       string `yaml:"title"`
			Description string `yaml:"description"`
			Template    string `yaml:"template"`
		}
		if err := yaml.Unmarshal([]byte(front), &meta); err != nil {
			t.Errorf("%s: %v", path, err)
		}
		out[slug] = sitePage{slug: slug, file: path, title: meta.Title, desc: meta.Description, splash: meta.Template == "splash", body: body}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out) == 0 {
		t.Fatal("the site has no pages")
	}
	return out
}

// skeleton reports whether a module has nothing yet but its manifest: no
// kinds, reconcilers, operations, hooks or anything else a module adds.
func skeleton(m modules.Module) bool {
	switch m.(type) {
	case modules.KindProvider, modules.ReconcilerProvider, modules.APIProvider, modules.HookProvider,
		modules.EventProvider, modules.MigrationProvider, modules.RecipeProvider, modules.BackupProvider,
		modules.HealthProvider, modules.OwnershipProvider, modules.AccountProvider, modules.HintProvider,
		modules.InboxProvider, modules.TransitionPlanner, modules.Starter, modules.Stopper:
		return false
	}
	return true
}

var (
	fencedCode       = regexp.MustCompile("(?ms)^```.*?^```")
	inlineCode       = regexp.MustCompile("`[^`\n]*`")
	placeholderWords = regexp.MustCompile(`(?i)\b(todo|tbd|coming soon|to be written|not yet documented|lorem ipsum)\b`)
)

// prose returns a page's text without its code.
func prose(body string) string {
	return inlineCode.ReplaceAllString(fencedCode.ReplaceAllString(body, ""), "")
}

// minWords is how much a written page says at least; the shortest page
// written so far has well over twice as many.
const minWords = 100

// TestEveryPageIsWritten fails on pages that are placeholders: only their
// description repeated, a few words, or words saying the page is still to
// come. The page of a module that has nothing but its manifest yet, and the
// migration guides while the migration module is one, are the only ones
// that may wait, for the work that builds what they describe.
func TestEveryPageIsWritten(t *testing.T) {
	waiting := map[string]string{}
	for _, m := range modules.Default().All() {
		id := m.Manifest().ID
		if !skeleton(m) {
			continue
		}
		waiting["modules/"+id] = "the " + id + " module"
		if id == "migration" {
			for slug := range pages(t) {
				if strings.HasPrefix(slug, "migration/") {
					waiting[slug] = "the migration module"
				}
			}
		}
	}
	for slug, p := range pages(t) {
		if p.title == "" || p.desc == "" {
			t.Errorf("%s needs a title and a description", p.file)
		}
		text := strings.TrimSpace(p.body)
		words := len(strings.Fields(prose(text)))
		placeholder := text == "" || text == strings.TrimSpace(p.desc) || (words < minWords && !p.splash)
		if m := placeholderWords.FindString(prose(text)); m != "" {
			t.Errorf("%s says %q, as if it were still to be written", p.file, m)
		}
		if _, wait := waiting[slug]; wait {
			continue
		}
		if placeholder {
			t.Errorf("%s is a placeholder (%d words); its subject is in the code, so write it", p.file, words)
		}
	}
}

// TestSidebarListsEveryPage holds the sidebar in astro.config.mjs to the
// pages: every page is in it once, and every entry is a page.
func TestSidebarListsEveryPage(t *testing.T) {
	config := read(t, "site/astro.config.mjs")
	start := strings.Index(config, "const sidebar = [")
	end := strings.Index(config[start:], "\n];")
	if start < 0 || end < 0 {
		t.Fatal("astro.config.mjs has no sidebar")
	}
	listed := map[string]int{}
	for _, m := range regexp.MustCompile(`"([a-z0-9-]+(?:/[a-z0-9-]+)*)"`).FindAllStringSubmatch(config[start:start+end], -1) {
		listed[m[1]]++
	}
	all := pages(t)
	for slug := range all {
		if slug == "index" {
			continue
		}
		switch listed[slug] {
		case 0:
			t.Errorf("the sidebar leaves out %s", slug)
		case 1:
		default:
			t.Errorf("the sidebar lists %s %d times", slug, listed[slug])
		}
	}
	for slug := range listed {
		if _, ok := all[slug]; !ok {
			t.Errorf("the sidebar lists %s, which has no page", slug)
		}
	}
}

var (
	markdownLink = regexp.MustCompile(`\]\(([^)\s]+)\)|\bhref="([^"]+)"`)
	heading      = regexp.MustCompile(`(?m)^#{2,6}\s+(.+?)\s*#*\s*$`)
	headingLink  = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
)

// anchors returns the IDs the site gives a page's headings, as Starlight
// makes them: lowercase, punctuation dropped, spaces as dashes, and a
// number after the ones that repeat.
func anchors(body string) map[string]bool {
	out := map[string]bool{"_top": true}
	seen := map[string]int{}
	for _, m := range heading.FindAllStringSubmatch(fencedCode.ReplaceAllString(body, ""), -1) {
		text := headingLink.ReplaceAllString(m[1], "$1")
		text = strings.NewReplacer("`", "", "*", "").Replace(text)
		var b strings.Builder
		for _, r := range strings.ToLower(text) {
			switch {
			case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_':
				b.WriteRune(r)
			case r == ' ':
				b.WriteByte('-')
			}
		}
		id := b.String()
		if n := seen[id]; n > 0 {
			out[id+"-"+strconv.Itoa(n)] = true
		} else {
			out[id] = true
		}
		seen[id]++
	}
	return out
}

// TestLinksLeadToPages follows every link between the site's pages, and to
// their headings, as the published site resolves them.
func TestLinksLeadToPages(t *testing.T) {
	all := pages(t)
	base, _ := url.Parse("https://docs.example/racksail/")
	for _, p := range all {
		self := base.JoinPath(p.slug) // the published page, a directory
		if p.slug == "index" {
			self = base
		} else {
			self.Path += "/"
		}
		for _, m := range markdownLink.FindAllStringSubmatch(fencedCode.ReplaceAllString(p.body, ""), -1) {
			link := m[1] + m[2]
			if strings.Contains(link, "://") || strings.HasPrefix(link, "mailto:") {
				continue
			}
			ref, err := url.Parse(link)
			if err != nil {
				t.Errorf("%s links to %s, which isn't a URL", p.file, link)
				continue
			}
			if strings.HasPrefix(link, "/") {
				t.Errorf("%s links to %s from the root of the host; links between pages are relative, so they work wherever the site is served", p.file, link)
				continue
			}
			target := self.ResolveReference(ref)
			slug, ok := strings.CutPrefix(target.Path, base.Path)
			if !ok {
				t.Errorf("%s links to %s, outside the site", p.file, link)
				continue
			}
			slug = strings.TrimSuffix(slug, "/")
			if slug == "" {
				slug = "index"
			}
			if ref.Path != "" && !strings.HasSuffix(ref.Path, "/") {
				t.Errorf("%s links to %s; pages are published as directories, so the link ends with a slash", p.file, link)
			}
			to, ok := all[slug]
			if !ok {
				t.Errorf("%s links to %s, and there is no page %s", p.file, link, slug)
				continue
			}
			if target.Fragment != "" && !anchors(to.body)[target.Fragment] {
				t.Errorf("%s links to %s, and %s has no heading #%s", p.file, link, slug, target.Fragment)
			}
		}
	}
}

var goProgram = regexp.MustCompile(`(?m)^package main$`)

// TestGoProgramsBuild builds every complete Go program the site shows, so
// people can copy them as they are.
func TestGoProgramsBuild(t *testing.T) {
	gobin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go isn't on the PATH")
	}
	var built []string
	all := pages(t)
	slugs := make([]string, 0, len(all))
	for slug := range all {
		slugs = append(slugs, slug)
	}
	sort.Strings(slugs)
	for _, slug := range slugs {
		for i, block := range codeBlocks(all[slug].body, "go") {
			if !goProgram.MatchString(block) {
				continue
			}
			dir := t.TempDir()
			files := map[string]string{"go.mod": "module example.com/program\n\ngo 1.22\n", "main.go": block + "\n"}
			for name, content := range files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command(gobin, "vet", ".")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local", "CGO_ENABLED=0", "GOFLAGS=-mod=mod")
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Errorf("the Go program in %s, block %d, doesn't build: %v\n%s", all[slug].file, i+1, err, out)
			}
			built = append(built, slug)
		}
	}
	if !slices.Contains(built, "addons/sdks") || !slices.Contains(built, "api/clients") {
		t.Errorf("built the programs of %v; the SDKs and clients pages each have one", built)
	}
}
