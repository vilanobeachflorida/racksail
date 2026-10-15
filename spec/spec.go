// Package spec holds Racksail's machine-readable specifications. The
// OpenAPI documents of the panel API (openapi.json), the agent protocol
// (agent-protocol.json) and the add-on protocol (addon-protocol.json) are
// generated from the code; the JSON Schemas in schemas/ describe the files
// people write: module manifests, themes, recipes, add-on manifests, account
// archives, rules and installer answers.
//
// The schemas are embedded so the binary validates those files itself.
package spec

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"go.yaml.in/yaml/v3"
)

//go:embed schemas/*.json
var schemas embed.FS

// Schema names.
const (
	Module  = "module"
	Theme   = "theme"
	Recipe  = "recipe"
	Addon   = "addon"
	Archive = "archive"
	Policy  = "policy"
	Answers = "answers"
)

// Names lists every schema.
func Names() []string { return []string{Module, Theme, Recipe, Addon, Archive, Policy, Answers} }

// SchemaJSON returns a schema document.
func SchemaJSON(name string) ([]byte, error) {
	return schemas.ReadFile("schemas/" + name + ".schema.json")
}

func compile(name string) (*jsonschema.Schema, error) {
	raw, err := SchemaJSON(name)
	if err != nil {
		return nil, fmt.Errorf("unknown schema %q", name)
	}
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("schema %s: %w", name, err)
	}
	url := "racksail:" + name + ".schema.json"
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	if err := c.AddResource(url, doc); err != nil {
		return nil, fmt.Errorf("schema %s: %w", name, err)
	}
	return c.Compile(url)
}

// Validate checks a document decoded from JSON or YAML against a schema.
func Validate(name string, doc any) error {
	sch, err := compile(name)
	if err != nil {
		return err
	}
	// A round trip through JSON gives the validator plain JSON values, so
	// YAML timestamps and integer types behave like their JSON forms.
	raw, err := json.Marshal(doc)
	if err != nil {
		return fmt.Errorf("encoding document: %w", err)
	}
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return err
	}
	if err := sch.Validate(inst); err != nil {
		return fmt.Errorf("%s doesn't match the %s schema: %w", describe(doc), name, err)
	}
	return nil
}

// ValidateYAML decodes YAML and checks it against a schema.
func ValidateYAML(name string, data []byte) error {
	var doc any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("reading YAML: %w", err)
	}
	return Validate(name, doc)
}

// ValidateJSON decodes JSON and checks it against a schema.
func ValidateJSON(name string, data []byte) error {
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("reading JSON: %w", err)
	}
	return Validate(name, doc)
}

func describe(doc any) string {
	if m, ok := doc.(map[string]any); ok {
		if id, ok := m["id"].(string); ok {
			return fmt.Sprintf("%q", id)
		}
	}
	return "the document"
}
