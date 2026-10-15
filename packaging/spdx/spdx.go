// Package spdx reads SPDX license expressions and knows which licenses are
// approved by the Open Source Initiative and which of those can be combined
// with Racksail's AGPL-3.0-or-later code.
//
// The release tooling uses it to check the licenses of every dependency, and
// the catalog tool to check that an item labelled open source really is.
package spdx

import (
	"fmt"
	"slices"
	"strings"
)

// Expr is a parsed license expression.
type Expr interface {
	String() string
}

// License is one license, optionally with "or later" (+) and an exception.
type License struct {
	ID        string
	Plus      bool
	Exception string
}

func (l License) String() string {
	s := l.ID
	if l.Plus {
		s += "+"
	}
	if l.Exception != "" {
		s += " WITH " + l.Exception
	}
	return s
}

// And requires both sides.
type And struct{ Left, Right Expr }

func (a And) String() string { return "(" + a.Left.String() + " AND " + a.Right.String() + ")" }

// Or offers a choice of either side.
type Or struct{ Left, Right Expr }

func (o Or) String() string { return "(" + o.Left.String() + " OR " + o.Right.String() + ")" }

// Parse reads an expression such as "MIT", "(MIT OR Apache-2.0)" or
// "GPL-2.0-or-later WITH Classpath-exception-2.0". Operators may be written
// in upper or lower case.
func Parse(s string) (Expr, error) {
	p := &parser{tokens: tokenize(s)}
	if len(p.tokens) == 0 {
		return nil, fmt.Errorf("the license expression is empty")
	}
	e, err := p.or()
	if err != nil {
		return nil, fmt.Errorf("license expression %q: %w", s, err)
	}
	if p.pos != len(p.tokens) {
		return nil, fmt.Errorf("license expression %q: unexpected %q", s, p.tokens[p.pos])
	}
	return e, nil
}

func tokenize(s string) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range s {
		switch {
		case r == '(' || r == ')':
			flush()
			out = append(out, string(r))
		case r == ' ' || r == '\t' || r == '\n' || r == '\r':
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

type parser struct {
	tokens []string
	pos    int
}

func (p *parser) peek() string {
	if p.pos < len(p.tokens) {
		return p.tokens[p.pos]
	}
	return ""
}

func isOp(tok, op string) bool { return strings.EqualFold(tok, op) }

func (p *parser) or() (Expr, error) {
	left, err := p.and()
	if err != nil {
		return nil, err
	}
	for isOp(p.peek(), "OR") {
		p.pos++
		right, err := p.and()
		if err != nil {
			return nil, err
		}
		left = Or{left, right}
	}
	return left, nil
}

func (p *parser) and() (Expr, error) {
	left, err := p.term()
	if err != nil {
		return nil, err
	}
	for isOp(p.peek(), "AND") {
		p.pos++
		right, err := p.term()
		if err != nil {
			return nil, err
		}
		left = And{left, right}
	}
	return left, nil
}

func (p *parser) term() (Expr, error) {
	tok := p.peek()
	switch {
	case tok == "":
		return nil, fmt.Errorf("a license is missing at the end")
	case tok == "(":
		p.pos++
		e, err := p.or()
		if err != nil {
			return nil, err
		}
		if p.peek() != ")" {
			return nil, fmt.Errorf("a closing parenthesis is missing")
		}
		p.pos++
		return e, nil
	case tok == ")" || isOp(tok, "AND") || isOp(tok, "OR") || isOp(tok, "WITH"):
		return nil, fmt.Errorf("expected a license, found %q", tok)
	}
	p.pos++
	l := License{ID: tok}
	if strings.HasSuffix(l.ID, "+") {
		l.ID, l.Plus = strings.TrimSuffix(l.ID, "+"), true
	}
	if !validID(l.ID) {
		return nil, fmt.Errorf("%q isn't a license identifier", tok)
	}
	if isOp(p.peek(), "WITH") {
		p.pos++
		exc := p.peek()
		if exc == "" || exc == "(" || exc == ")" || !validID(exc) {
			return nil, fmt.Errorf("WITH needs an exception identifier")
		}
		p.pos++
		l.Exception = exc
	}
	return l, nil
}

func validID(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		ok := r == '-' || r == '.' || r == ':' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !ok {
			return false
		}
	}
	return true
}

// Licenses lists every license in the expression, in order.
func Licenses(e Expr) []License {
	switch x := e.(type) {
	case License:
		return []License{x}
	case And:
		return append(Licenses(x.Left), Licenses(x.Right)...)
	case Or:
		return append(Licenses(x.Left), Licenses(x.Right)...)
	}
	return nil
}

// Satisfied reports whether the expression can be met with allowed licenses
// only: both sides of AND must be allowed, and one side of OR is enough.
func Satisfied(e Expr, allowed func(License) bool) bool {
	switch x := e.(type) {
	case License:
		return allowed(x)
	case And:
		return Satisfied(x.Left, allowed) && Satisfied(x.Right, allowed)
	case Or:
		return Satisfied(x.Left, allowed) || Satisfied(x.Right, allowed)
	}
	return false
}

// deprecated maps the GNU identifiers SPDX deprecated to their current
// forms, without and with "or later".
var deprecated = map[string][2]string{
	"gpl-1.0":  {"GPL-1.0-only", "GPL-1.0-or-later"},
	"gpl-2.0":  {"GPL-2.0-only", "GPL-2.0-or-later"},
	"gpl-3.0":  {"GPL-3.0-only", "GPL-3.0-or-later"},
	"lgpl-2.0": {"LGPL-2.0-only", "LGPL-2.0-or-later"},
	"lgpl-2.1": {"LGPL-2.1-only", "LGPL-2.1-or-later"},
	"lgpl-3.0": {"LGPL-3.0-only", "LGPL-3.0-or-later"},
	"agpl-3.0": {"AGPL-3.0-only", "AGPL-3.0-or-later"},
}

var lowerOSI = func() map[string]string {
	m := make(map[string]string, len(osiApproved))
	for _, id := range osiApproved {
		m[strings.ToLower(id)] = id
	}
	return m
}()

// Normalize spells a license the way SPDX does today: letter case fixed,
// deprecated GNU identifiers such as GPL-2.0+ turned into GPL-2.0-or-later,
// and a "+" on a license that covers one version, such as LGPL-2.1-only+,
// turned into its "or later" form.
func Normalize(l License) License {
	key := strings.ToLower(l.ID)
	if forms, ok := deprecated[key]; ok {
		l.ID = forms[0]
		if l.Plus {
			l.ID = forms[1]
		}
		l.Plus = false
		return l
	}
	if c, ok := lowerOSI[key]; ok {
		l.ID = c
	}
	if l.Plus && strings.HasSuffix(l.ID, "-only") {
		l.ID = strings.TrimSuffix(l.ID, "-only") + "-or-later"
		l.Plus = false
	}
	return l
}

// OSIApproved reports whether id names a license approved by the Open
// Source Initiative.
func OSIApproved(id string) bool {
	_, ok := slices.BinarySearch(osiApproved, Normalize(License{ID: id}).ID)
	return ok
}

// AGPLCompatible reports whether code under the license can be combined with
// AGPL-3.0-or-later code and distributed as one program.
func AGPLCompatible(l License) bool {
	l = Normalize(l)
	if l.Exception != "" && !slices.Contains(compatibleExceptions, l.Exception) {
		return false
	}
	_, ok := slices.BinarySearch(agplCompatible, l.ID)
	return ok
}

// Allowed reports whether a dependency under the license may be part of
// Racksail: the license is approved by the Open Source Initiative and its
// code can be combined with AGPL-3.0-or-later code.
func Allowed(l License) bool { return OSIApproved(l.ID) && AGPLCompatible(l) }

// compatibleExceptions only add permissions, so they never make a license
// incompatible.
var compatibleExceptions = []string{
	"Classpath-exception-2.0", "GCC-exception-3.1", "LLVM-exception", "Autoconf-exception-3.0", "Bison-exception-2.2",
}

// agplCompatible lists the OSI-approved licenses whose code may be combined
// with AGPL-3.0-or-later code, following the Free Software Foundation's list
// of GPLv3-compatible licenses. OFL-1.1 is here for the bundled fonts, which
// ship as separate files rather than being combined with code. It is sorted
// for binary search.
var agplCompatible = []string{
	"0BSD",
	"AGPL-3.0-only",
	"AGPL-3.0-or-later",
	"Apache-2.0",
	"Artistic-2.0",
	"BSD-1-Clause",
	"BSD-2-Clause",
	"BSD-2-Clause-Patent",
	"BSD-3-Clause",
	"BSL-1.0",
	"BlueOak-1.0.0",
	"EUPL-1.2",
	"GPL-2.0-or-later",
	"GPL-3.0-only",
	"GPL-3.0-or-later",
	"HPND",
	"ISC",
	"LGPL-2.0-only",
	"LGPL-2.0-or-later",
	"LGPL-2.1-only",
	"LGPL-2.1-or-later",
	"LGPL-3.0-only",
	"LGPL-3.0-or-later",
	"MIT",
	"MIT-0",
	"MIT-Modern-Variant",
	"MPL-2.0",
	"NCSA",
	"NTP",
	"OFL-1.1",
	"PostgreSQL",
	"Python-2.0",
	"UPL-1.0",
	"Unicode-3.0",
	"Unicode-DFS-2016",
	"Unlicense",
	"W3C",
	"ZPL-2.0",
	"ZPL-2.1",
	"Zlib",
}

// osiApproved lists the SPDX identifiers of licenses approved by the Open
// Source Initiative. It is sorted for binary search.
var osiApproved = []string{
	"0BSD",
	"AAL",
	"AFL-3.0",
	"AGPL-3.0-only",
	"AGPL-3.0-or-later",
	"APL-1.0",
	"APSL-2.0",
	"Apache-1.1",
	"Apache-2.0",
	"Artistic-1.0",
	"Artistic-2.0",
	"BSD-1-Clause",
	"BSD-2-Clause",
	"BSD-2-Clause-Patent",
	"BSD-3-Clause",
	"BSD-3-Clause-LBNL",
	"BSL-1.0",
	"BlueOak-1.0.0",
	"CAL-1.0",
	"CATOSL-1.1",
	"CDDL-1.0",
	"CECILL-2.1",
	"CERN-OHL-P-2.0",
	"CERN-OHL-S-2.0",
	"CERN-OHL-W-2.0",
	"CNRI-Python",
	"CPAL-1.0",
	"CUA-OPL-1.0",
	"ECL-1.0",
	"ECL-2.0",
	"EFL-2.0",
	"EPL-1.0",
	"EPL-2.0",
	"EUDatagrid",
	"EUPL-1.1",
	"EUPL-1.2",
	"Entessa",
	"Fair",
	"Frameworx-1.0",
	"GPL-2.0-only",
	"GPL-2.0-or-later",
	"GPL-3.0-only",
	"GPL-3.0-or-later",
	"HPND",
	"IPA",
	"IPL-1.0",
	"ISC",
	"Intel",
	"LGPL-2.0-only",
	"LGPL-2.0-or-later",
	"LGPL-2.1-only",
	"LGPL-2.1-or-later",
	"LGPL-3.0-only",
	"LGPL-3.0-or-later",
	"LPL-1.0",
	"LPL-1.02",
	"LPPL-1.3c",
	"LiLiQ-P-1.1",
	"LiLiQ-R-1.1",
	"LiLiQ-Rplus-1.1",
	"MIT",
	"MIT-0",
	"MIT-Modern-Variant",
	"MPL-1.0",
	"MPL-1.1",
	"MPL-2.0",
	"MPL-2.0-no-copyleft-exception",
	"MS-PL",
	"MS-RL",
	"MirOS",
	"Motosoto",
	"MulanPSL-2.0",
	"Multics",
	"NASA-1.3",
	"NCSA",
	"NGPL",
	"NPOSL-3.0",
	"NTP",
	"Naumen",
	"Nokia",
	"OCLC-2.0",
	"OFL-1.1",
	"OGTSL",
	"OLDAP-2.8",
	"OSL-1.0",
	"OSL-2.0",
	"OSL-2.1",
	"OSL-3.0",
	"PHP-3.0",
	"PHP-3.01",
	"PostgreSQL",
	"Python-2.0",
	"QPL-1.0",
	"RPL-1.1",
	"RPL-1.5",
	"RPSL-1.0",
	"RSCPL",
	"SISSL",
	"SPL-1.0",
	"SimPL-2.0",
	"Sleepycat",
	"UCL-1.0",
	"UPL-1.0",
	"Unicode-3.0",
	"Unicode-DFS-2016",
	"Unlicense",
	"VSL-1.0",
	"W3C",
	"Watcom-1.0",
	"Xnet",
	"ZPL-2.0",
	"ZPL-2.1",
	"Zlib",
}
