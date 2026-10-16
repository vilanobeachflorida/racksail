package updates

import "testing"

// Release tags come from GitHub and mirrors. Whatever a tag says, reading
// it never panics, a version reads back as itself, and versions are
// ordered the same way both ways round.
func FuzzParseVersion(f *testing.F) {
	for _, s := range []string{"1.2.3", "v1.5.0-beta.1", "1.5.0-alpha.nightly.20261015.42", "1.2.3+20261015", "01.2.3", "1.2", "v1.5.0-rc.1.nightly.20261020.47"} {
		f.Add(s, "1.4.2")
	}
	f.Fuzz(func(t *testing.T, a, b string) {
		va, err := ParseVersion(a)
		if err != nil {
			return
		}
		again, err := ParseVersion(va.String())
		if err != nil || again != va {
			t.Fatalf("%q reads back as %+v, %v", a, again, err)
		}
		_, _ = ChannelOf(a)
		vb, err := ParseVersion(b)
		if err != nil {
			return
		}
		if va.Compare(vb) != -vb.Compare(va) {
			t.Fatalf("%s and %s compare %d and %d", va, vb, va.Compare(vb), vb.Compare(va))
		}
	})
}
