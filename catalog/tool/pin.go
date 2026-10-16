package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strconv"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"github.com/vilanobeachflorida/racksail/internal/recipes"
)

// maxDownload bounds what pinning reads, model files included.
const maxDownload = 64 << 30

// PinResult is what pinning found for one download.
type PinResult struct {
	URL    string
	SHA256 string
	Size   int64
	// Changed is true when the recipe listed another checksum or size.
	Changed bool
}

// Pin fetches every download a recipe lists and compares its checksum and
// size with the recipe's. With write it corrects them in the file, changing
// nothing else, which is how version bumps get their new checksums.
func Pin(ctx context.Context, client *http.Client, file string, write bool) ([]PinResult, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	if _, err := recipes.Parse(data); err != nil {
		return nil, err
	}
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	var results []PinResult
	var edits []edit
	for _, d := range downloadNodes(&doc) {
		url, sum, size := mapValue(d, "url"), mapValue(d, "sha256"), mapValue(d, "size")
		if url == nil || sum == nil || size == nil {
			return nil, fmt.Errorf("%s: a download needs url, sha256 and size", file)
		}
		gotSum, gotSize, err := fetch(ctx, client, url.Value)
		if err != nil {
			return nil, err
		}
		res := PinResult{URL: url.Value, SHA256: gotSum, Size: gotSize}
		if sum.Value != gotSum {
			res.Changed = true
			edits = append(edits, edit{sum.Line, sum.Column, scalarText(gotSum)})
		}
		if size.Value != strconv.FormatInt(gotSize, 10) {
			res.Changed = true
			edits = append(edits, edit{size.Line, size.Column, strconv.FormatInt(gotSize, 10)})
		}
		results = append(results, res)
	}
	if write && len(edits) > 0 {
		out, err := applyEdits(data, edits)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		if _, err := recipes.Parse(out); err != nil {
			return nil, fmt.Errorf("%s: the corrected recipe doesn't parse: %w", file, err)
		}
		if err := os.WriteFile(file, out, 0o644); err != nil {
			return nil, err
		}
	}
	return results, nil
}

func downloadNodes(doc *yaml.Node) []*yaml.Node {
	if len(doc.Content) == 0 {
		return nil
	}
	seq := mapValue(doc.Content[0], "downloads")
	if seq == nil || seq.Kind != yaml.SequenceNode {
		return nil
	}
	return seq.Content
}

func mapValue(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func fetch(ctx context.Context, client *http.Client, url string) (string, int64, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", 0, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", 0, err
	}
	defer resp.Body.Close()
	if resp.Request.URL.Scheme != "https" {
		return "", 0, fmt.Errorf("%s redirected to %s, which isn't https", url, resp.Request.URL)
	}
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("%s: %s", url, resp.Status)
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(resp.Body, maxDownload+1))
	if err != nil {
		return "", 0, fmt.Errorf("%s: %w", url, err)
	}
	if n > maxDownload {
		return "", 0, fmt.Errorf("%s is larger than %d bytes", url, int64(maxDownload))
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// scalarText writes a checksum as a plain scalar unless YAML would read it
// as something other than a string, such as a number made of digits.
func scalarText(s string) string {
	var v any
	if err := yaml.Unmarshal([]byte(s), &v); err == nil {
		if _, ok := v.(string); ok {
			return s
		}
	}
	return strconv.Quote(s)
}

// edit replaces the scalar starting at a line and column, both counted from
// one, as the YAML parser reports them.
type edit struct {
	line, column int
	text         string
}

func applyEdits(data []byte, edits []edit) ([]byte, error) {
	type span struct {
		start, end int
		text       string
	}
	var spans []span
	for _, e := range edits {
		start, err := offset(data, e.line, e.column)
		if err != nil {
			return nil, err
		}
		spans = append(spans, span{start, scalarEnd(data, start), e.text})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].start > spans[j].start })
	out := bytes.Clone(data)
	for _, s := range spans {
		out = append(out[:s.start], append([]byte(s.text), out[s.end:]...)...)
	}
	return out, nil
}

func offset(data []byte, line, column int) (int, error) {
	pos := 0
	for l := 1; l < line; l++ {
		i := bytes.IndexByte(data[pos:], '\n')
		if i < 0 {
			return 0, errors.New("line out of range")
		}
		pos += i + 1
	}
	for c := 1; c < column; c++ {
		if pos >= len(data) || data[pos] == '\n' {
			return 0, errors.New("column out of range")
		}
		_, size := utf8.DecodeRune(data[pos:])
		pos += size
	}
	return pos, nil
}

// scalarEnd finds where a quoted or plain scalar ends.
func scalarEnd(data []byte, start int) int {
	if start < len(data) && (data[start] == '"' || data[start] == '\'') {
		if i := bytes.IndexByte(data[start+1:], data[start]); i >= 0 {
			return start + 1 + i + 1
		}
		return len(data)
	}
	end := start
	for end < len(data) {
		switch data[end] {
		case ' ', '\t', '\r', '\n', ',', '}', ']', '#':
			return end
		}
		end++
	}
	return end
}
