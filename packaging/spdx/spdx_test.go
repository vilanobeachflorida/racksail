package spdx

import (
	"slices"
	"testing"
)

func TestListsAreSorted(t *testing.T) {
	if !slices.IsSorted(osiApproved) {
		t.Error("osiApproved must be sorted for binary search")
	}
	if !slices.IsSorted(agplCompatible) {
		t.Error("agplCompatible must be sorted for binary search")
	}
	for _, id := range agplCompatible {
		if !OSIApproved(id) {
			t.Errorf("%s is listed as compatible but isn't OSI-approved", id)
		}
	}
}

func TestParse(t *testing.T) {
	cases := map[string]string{
		"MIT":                                      "MIT",
		"(MIT OR Apache-2.0)":                      "(MIT OR Apache-2.0)",
		"MIT OR Apache-2.0 AND BSD-3-Clause":       "(MIT OR (Apache-2.0 AND BSD-3-Clause))",
		"(MIT OR Apache-2.0) AND BSD-3-Clause":     "((MIT OR Apache-2.0) AND BSD-3-Clause)",
		"GPL-2.0+":                                 "GPL-2.0+",
		"Apache-2.0 WITH LLVM-exception":           "Apache-2.0 WITH LLVM-exception",
		"mit or isc":                               "(mit OR isc)",
		"LicenseRef-Proprietary":                   "LicenseRef-Proprietary",
		"Apache-2.0 AND LGPL-3.0-or-later AND MIT": "((Apache-2.0 AND LGPL-3.0-or-later) AND MIT)",
	}
	for in, want := range cases {
		e, err := Parse(in)
		if err != nil {
			t.Errorf("Parse(%q): %v", in, err)
			continue
		}
		if e.String() != want {
			t.Errorf("Parse(%q) = %s, want %s", in, e, want)
		}
	}
	for _, bad := range []string{"", "MIT OR", "(MIT", "MIT)", "AND MIT", "MIT WITH", "MIT/Apache", "MIT Apache-2.0"} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("Parse(%q) should fail", bad)
		}
	}
}

func TestAllowed(t *testing.T) {
	cases := map[string]bool{
		"MIT":                 true,
		"mit":                 true,
		"Apache-2.0":          true,
		"(MIT OR Apache-2.0)": true,
		"MPL-2.0":             true,
		"LGPL-3.0-or-later":   true,
		"Apache-2.0 AND LGPL-3.0-or-later AND MIT": true,
		"Apache-2.0 WITH LLVM-exception":           true,
		"GPL-2.0-only":                             false,
		"GPL-2.0+":                                 true,
		"GPL-2.0":                                  false,
		"LGPL-2.1+":                                true,
		"GPL-2.0-only+":                            true,
		"GPL-2.0-only OR MIT":                      true,
		"GPL-2.0-only AND MIT":                     false,
		"EPL-2.0":                                  false,
		"CDDL-1.0":                                 false,
		"CC0-1.0":                                  false,
		"(MIT OR CC0-1.0)":                         true,
		"LicenseRef-Proprietary":                   false,
		"SSPL-1.0":                                 false,
		"MIT WITH Some-exception":                  false,
	}
	for expr, want := range cases {
		e, err := Parse(expr)
		if err != nil {
			t.Fatalf("Parse(%q): %v", expr, err)
		}
		if got := Satisfied(e, Allowed); got != want {
			t.Errorf("%s: allowed = %v, want %v", expr, got, want)
		}
	}
}

func TestOSIApproved(t *testing.T) {
	for id, want := range map[string]bool{"MIT": true, "apache-2.0": true, "EPL-2.0": true, "GPL-2.0": true, "BUSL-1.1": false, "CC-BY-4.0": false} {
		if got := OSIApproved(id); got != want {
			t.Errorf("OSIApproved(%s) = %v, want %v", id, got, want)
		}
	}
}

func TestLicenses(t *testing.T) {
	e, _ := Parse("(MIT OR Apache-2.0) AND BSD-3-Clause")
	var ids []string
	for _, l := range Licenses(e) {
		ids = append(ids, l.ID)
	}
	if !slices.Equal(ids, []string{"MIT", "Apache-2.0", "BSD-3-Clause"}) {
		t.Errorf("Licenses = %v", ids)
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range []string{"MIT", "(MIT OR Apache-2.0) AND BSD-3-Clause", "GPL-2.0+ WITH Classpath-exception-2.0", "((("} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		e, err := Parse(s)
		if err != nil {
			return
		}
		// What parses prints as an expression that parses to the same thing.
		again, err := Parse(e.String())
		if err != nil {
			t.Fatalf("Parse(%q) = %s, which doesn't parse: %v", s, e, err)
		}
		if again.String() != e.String() {
			t.Fatalf("round trip of %q: %s != %s", s, again, e)
		}
	})
}
