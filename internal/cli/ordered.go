package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"go.yaml.in/yaml/v3"
)

// object is a JSON object that keeps its keys in the order the panel sent
// them, which is the order its types declare their fields in: what reads
// best in tables and YAML.
type object struct {
	keys   []string
	values map[string]any
}

func (o *object) get(key string) any {
	if o == nil {
		return nil
	}
	return o.values[key]
}

func (o *object) set(key string, v any) {
	if _, ok := o.values[key]; !ok {
		o.keys = append(o.keys, key)
	}
	o.values[key] = v
}

// MarshalJSON writes the object with its keys in order.
func (o *object) MarshalJSON() ([]byte, error) {
	var b bytes.Buffer
	b.WriteByte('{')
	for i, k := range o.keys {
		if i > 0 {
			b.WriteByte(',')
		}
		key, _ := json.Marshal(k)
		b.Write(key)
		b.WriteByte(':')
		v, err := json.Marshal(o.values[k])
		if err != nil {
			return nil, err
		}
		b.Write(v)
	}
	b.WriteByte('}')
	return b.Bytes(), nil
}

// decodeOrdered reads JSON, keeping the order of objects' keys and numbers
// as they were written.
func decodeOrdered(raw []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	v, err := readValue(dec)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, fmt.Errorf("unexpected data after the JSON value")
	}
	return v, nil
}

func readValue(dec *json.Decoder) (any, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return tok, nil
	}
	switch delim {
	case '{':
		o := &object{values: map[string]any{}}
		for dec.More() {
			keyTok, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, _ := keyTok.(string)
			v, err := readValue(dec)
			if err != nil {
				return nil, err
			}
			o.set(key, v)
		}
		_, err := dec.Token()
		return o, err
	case '[':
		list := []any{}
		for dec.More() {
			v, err := readValue(dec)
			if err != nil {
				return nil, err
			}
			list = append(list, v)
		}
		_, err := dec.Token()
		return list, err
	}
	return nil, fmt.Errorf("unexpected %v", delim)
}

// writeJSON writes a JSON answer indented, as the panel ordered it.
func writeJSON(w io.Writer, raw []byte) error {
	var b bytes.Buffer
	if err := json.Indent(&b, bytes.TrimSpace(raw), "", "  "); err != nil {
		return err
	}
	b.WriteByte('\n')
	_, err := w.Write(b.Bytes())
	return err
}

// writeYAML writes a JSON answer as YAML, keeping the order of its keys.
func writeYAML(w io.Writer, raw []byte) error {
	v, err := decodeOrdered(raw)
	if err != nil {
		return err
	}
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	if err := enc.Encode(yamlNode(v)); err != nil {
		return err
	}
	return enc.Close()
}

func yamlNode(v any) *yaml.Node {
	switch t := v.(type) {
	case *object:
		n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, k := range t.keys {
			n.Content = append(n.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: k}, yamlNode(t.values[k]))
		}
		return n
	case []any:
		n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, item := range t {
			n.Content = append(n.Content, yamlNode(item))
		}
		return n
	case string:
		n := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: t}
		if strings.Contains(t, "\n") {
			n.Style = yaml.LiteralStyle
		}
		return n
	case json.Number:
		tag := "!!int"
		if strings.ContainsAny(t.String(), ".eE") {
			tag = "!!float"
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: t.String()}
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: fmt.Sprint(t)}
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
}
