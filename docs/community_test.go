package docs

import (
	"os"
	"regexp"
	"strings"
	"testing"
)

// contentDir holds the documentation site's pages.
const contentDir = "site/src/content/docs"

// page returns the file of a page of the site, by its slug.
func page(slug string) string {
	if _, err := os.Stat(contentDir + "/" + slug + ".mdx"); err == nil {
		return contentDir + "/" + slug + ".mdx"
	}
	return contentDir + "/" + slug + ".md"
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.ReplaceAll(string(data), "\r\n", "\n")
}

// section returns what follows a second-level heading of a Markdown
// document, up to the next one.
func section(md, heading string) string {
	_, rest, found := strings.Cut(md, "\n## "+heading+"\n")
	if !found {
		return ""
	}
	if end := strings.Index(rest, "\n## "); end >= 0 {
		rest = rest[:end]
	}
	return rest
}

var emailAddress = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.[A-Za-z]{2,}`)

// TestCodeOfConductHasAPrivateContact checks that people can report a
// problem privately: the code of conduct gives an address that reaches the
// maintainers, and GOVERNANCE.md and the site's community pages give the
// same one.
func TestCodeOfConductHasAPrivateContact(t *testing.T) {
	enforcement := section(read(t, "../CODE_OF_CONDUCT.md"), "Enforcement")
	if enforcement == "" {
		t.Fatal("CODE_OF_CONDUCT.md has no Enforcement section")
	}
	found := emailAddress.FindAllString(enforcement, -1)
	if len(found) == 0 {
		t.Fatal("the code of conduct gives no address to report a problem to, so nobody can report one privately")
	}
	contact := found[0]
	for _, other := range found {
		if other != contact {
			t.Errorf("the code of conduct gives two addresses to report to: %s and %s", contact, other)
		}
	}
	for _, path := range []string{"../GOVERNANCE.md", page("community/code-of-conduct"), page("community/governance")} {
		if !strings.Contains(read(t, path), contact) {
			t.Errorf("%s doesn't give %s, where the code of conduct says reports go", path, contact)
		}
	}
}
