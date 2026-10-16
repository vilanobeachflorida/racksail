package ldap

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/vilanobeachflorida/racksail/addons/ldap-sign-in/internal/ber"
)

func TestEncodeFilter(t *testing.T) {
	got, err := EncodeFilter("(uid=ada)")
	if err != nil {
		t.Fatal(err)
	}
	want := []byte{0xa3, 0x0a, 0x04, 0x03, 'u', 'i', 'd', 0x04, 0x03, 'a', 'd', 'a'}
	if !bytes.Equal(got, want) {
		t.Errorf("(uid=ada) is % x, want % x", got, want)
	}

	got, err = EncodeFilter(`(&(objectClass=person)(|(cn=Ad*Lov*ce)(!(mail=*)))(uid=a\2ab))`)
	if err != nil {
		t.Fatal(err)
	}
	and, content, rest, err := ber.Next(got)
	if err != nil || and != FilterAnd || len(rest) != 0 {
		t.Fatalf("not an and: %#x %v", and, err)
	}
	var tags []byte
	for len(content) > 0 {
		var tag byte
		tag, _, content, err = ber.Next(content)
		if err != nil {
			t.Fatal(err)
		}
		tags = append(tags, tag)
	}
	if !bytes.Equal(tags, []byte{FilterEquality, FilterOr, FilterEquality}) {
		t.Errorf("the and holds % x", tags)
	}
	if !bytes.Contains(got, []byte{0x04, 0x03, 'a', '*', 'b'}) {
		t.Errorf(`\2a is a literal *: % x`, got)
	}
	sub := []byte{0x30, 0x0d, SubstringInitial, 2, 'A', 'd', SubstringAny, 3, 'L', 'o', 'v', SubstringFinal, 2, 'c', 'e'}
	if !bytes.Contains(got, sub) {
		t.Errorf("Ad*Lov*ce should be initial, any and final substrings: % x", got)
	}
	if !bytes.Contains(got, []byte{FilterNot, 0x06, FilterPresent, 0x04, 'm', 'a', 'i', 'l'}) {
		t.Errorf("(!(mail=*)) is not present: % x", got)
	}

	for _, ok := range []string{
		"(cn=*)", "(uid>=a)", "(uid<=a)", "(cn~=ada)", " (uid=ada) ", "(2.5.4.3=ada)", "(cn;lang-en=ada)",
		"(cn=*ada)", "(cn=ada*)", "(cn=*ad*)", "(|(uid=a))",
	} {
		if _, err := EncodeFilter(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	deep := strings.Repeat("(!", 20) + "(uid=a)" + strings.Repeat(")", 20)
	for _, bad := range []string{
		"", "uid=ada", "(uid=ada", "(uid=ada))", "(&)", "(uid=a(b)", "(=a)", "(uid~a)", `(uid=\4)`, `(uid=\zz)`,
		"(cn=**)", "(c n=a)", "(uid=ada)(cn=b)", deep,
	} {
		if _, err := EncodeFilter(bad); !errors.Is(err, ErrFilter) {
			t.Errorf("%q: %v", bad, err)
		}
	}
}

func TestEscapeFilter(t *testing.T) {
	got := EscapeFilter("a*b(c)\\d\x00e\x7fé")
	if want := `a\2ab\28c\29\5cd\00e\7fé`; got != want {
		t.Errorf("EscapeFilter = %s, want %s", got, want)
	}
	// What someone types is only ever one value.
	f, err := EncodeFilter("(uid=" + EscapeFilter("*)(uid=*") + ")")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(f, []byte("*)(uid=*")) || f[0] != FilterEquality {
		t.Errorf("an escaped value changed the filter: % x", f)
	}
}

func TestEscapeDN(t *testing.T) {
	for in, want := range map[string]string{
		"ada":             "ada",
		"Lovelace, Ada":   `Lovelace\, Ada`,
		" ada":            `\ ada`,
		"ada ":            `ada\ `,
		"#ada":            `\#ada`,
		"a#da":            "a#da",
		`a+b=c<d>e;f"g\h`: `a\+b\=c\<d\>e\;f\"g\\h`,
		"a\x00b":          `a\00b`,
	} {
		if got := EscapeDN(in); got != want {
			t.Errorf("EscapeDN(%q) = %s, want %s", in, got, want)
		}
	}
}

func FuzzEncodeFilter(f *testing.F) {
	for _, s := range []string{"(uid=ada)", "(&(a=b)(|(c=d*e)(!(f=*))))", `(cn=\2a)`, "(x>=1)"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		out, err := EncodeFilter(s)
		if err != nil {
			return
		}
		if _, _, rest, err := ber.Next(out); err != nil || len(rest) != 0 {
			t.Fatalf("%q encodes to something that doesn't read back: % x, %v", s, out, err)
		}
	})
}
