package fixtures

import (
	"os"
	"path/filepath"
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

// Each invalid answers file breaks one rule, which the installer catches.
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
		if _, err := installer.ParseAnswers(data); err == nil {
			t.Errorf("%s was accepted", f)
		}
	}
}
