package fixtures

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/installer"
)

// The answers files the harness uses are ones the installer accepts.
func TestAnswersAreValid(t *testing.T) {
	files, err := filepath.Glob("answers/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no answers files: %v", err)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := installer.ParseAnswers(data); err != nil {
			t.Errorf("%s: %v", f, err)
		}
	}
}

// wantLine names part of the error an invalid answers file must cause.
var wantLine = regexp.MustCompile(`(?m)^# want: (.+)$`)

// Each invalid answers file breaks one rule, which the installer catches
// for the reason its "# want:" line gives, so a file that starts failing
// for another reason, such as a field becoming required, fails the test.
func TestInvalidAnswersAreRefused(t *testing.T) {
	files, err := filepath.Glob("answers/invalid/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatalf("no invalid answers files: %v", err)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		m := wantLine.FindSubmatch(data)
		if m == nil {
			t.Errorf("%s has no \"# want:\" line naming the error it must cause", f)
			continue
		}
		want := strings.TrimSpace(string(m[1]))
		_, err = installer.ParseAnswers(data)
		switch {
		case err == nil:
			t.Errorf("%s was accepted", f)
		case !strings.Contains(err.Error(), want):
			t.Errorf("%s was refused for another reason than %q:\n%v", f, want, err)
		}
	}
}
