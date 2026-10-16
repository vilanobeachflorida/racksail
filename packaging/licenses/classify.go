package main

import (
	"regexp"
	"strings"
)

// A rule recognizes a license by phrases its text always has, and tells it
// apart from similar licenses by phrases they have and it doesn't. Headers
// are matched near the start of the text only, since longer licenses
// mention other licenses further down.
type rule struct {
	id     string
	header string
	all    []string
	none   []string
}

var rules = []rule{
	{id: "AGPL-3.0-only", header: "gnu affero general public license version 3"},
	{id: "GPL-3.0-only", header: "gnu general public license version 3"},
	{id: "GPL-2.0-only", header: "gnu general public license version 2"},
	{id: "LGPL-3.0-only", header: "gnu lesser general public license version 3"},
	{id: "LGPL-2.1-only", header: "gnu lesser general public license version 2.1"},
	{id: "MPL-2.0", header: "mozilla public license version 2.0"},
	{id: "MPL-2.0", all: []string{"this source code form is subject to the terms of the mozilla public license, v. 2.0"}},
	{id: "Apache-2.0", all: []string{"apache license", "version 2.0", "terms and conditions for use, reproduction, and distribution"}},
	{id: "Apache-2.0", all: []string{"licensed under the apache license, version 2.0"}},
	{id: "MIT", all: []string{"permission is hereby granted, free of charge, to any person obtaining a copy", "the above copyright notice and this permission notice shall be included"}},
	{id: "MIT-0", all: []string{"permission is hereby granted, free of charge, to any person obtaining a copy"}, none: []string{"the above copyright notice and this permission notice shall be included"}},
	{id: "BSD-3-Clause", all: []string{"redistribution and use in source and binary forms", "endorse or promote products derived from this software"}, none: []string{"all advertising materials"}},
	{id: "BSD-2-Clause", all: []string{"redistribution and use in source and binary forms"}, none: []string{"endorse or promote products derived from this software", "all advertising materials"}},
	{id: "BSD-4-Clause", all: []string{"redistribution and use in source and binary forms", "all advertising materials"}},
	{id: "ISC", all: []string{"permission to use, copy, modify, and", "distribute this software for any purpose with or without fee is hereby granted", "notice and this permission notice appear in all copies"}},
	{id: "0BSD", all: []string{"permission to use, copy, modify, and", "distribute this software for any purpose with or without fee is hereby granted"}, none: []string{"notice and this permission notice appear in all copies"}},
	{id: "Unlicense", all: []string{"this is free and unencumbered software released into the public domain"}},
	{id: "BSL-1.0", all: []string{"boost software license", "version 1.0"}},
	{id: "Zlib", all: []string{"this software is provided 'as-is', without any express or implied warranty", "altered source versions must be plainly marked as such"}},
	{id: "BlueOak-1.0.0", all: []string{"blue oak model license"}},
	{id: "CC0-1.0", all: []string{"cc0 1.0 universal"}},
}

// headerWindow is how far into a text a header is looked for.
const headerWindow = 300

var (
	markup     = strings.NewReplacer("*", " ", "#", " ", "`", " ", "‘", "'", "’", "'", "“", "\"", "”", "\"", " ", " ")
	whitespace = regexp.MustCompile(`\s+`)
)

func normalize(text string) string {
	return strings.TrimSpace(whitespace.ReplaceAllString(markup.Replace(strings.ToLower(text)), " "))
}

// Classify names the licenses in a license file, in the order of the rules.
// A file holding several licenses, such as one covering ported code, names
// each of them.
func Classify(text string) []string {
	norm := normalize(text)
	head := norm
	if len(head) > headerWindow {
		head = head[:headerWindow]
	}
	var found []string
	for _, r := range rules {
		if r.header != "" && !strings.Contains(head, r.header) {
			continue
		}
		if !containsAll(norm, r.all) || containsAny(norm, r.none) {
			continue
		}
		if !contains(found, r.id) {
			found = append(found, r.id)
		}
	}
	return found
}

func containsAll(s string, subs []string) bool {
	for _, sub := range subs {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}

func containsAny(s string, subs []string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}
