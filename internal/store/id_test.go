package store

import (
	"strings"
	"testing"
	"time"
	"uuid"
)

// Vectors from the TypeID specification (spec/valid.yml and spec/invalid.yml).
func TestIDSpecVectors(t *testing.T) {
	valid := []struct{ id, prefix, uuid string }{
		{"00000000000000000000000000", "", "00000000-0000-0000-0000-000000000000"},
		{"00000000000000000000000001", "", "00000000-0000-0000-0000-000000000001"},
		{"0000000000000000000000000a", "", "00000000-0000-0000-0000-00000000000a"},
		{"0000000000000000000000000g", "", "00000000-0000-0000-0000-000000000010"},
		{"00000000000000000000000010", "", "00000000-0000-0000-0000-000000000020"},
		{"7zzzzzzzzzzzzzzzzzzzzzzzzz", "", "ffffffff-ffff-ffff-ffff-ffffffffffff"},
		{"prefix_0123456789abcdefghjkmnpqrs", "prefix", "0110c853-1d09-52d8-d73e-1194e95b5f19"},
		{"prefix_01h455vb4pex5vsknk084sn02q", "prefix", "01890a5d-ac96-774b-bcce-b302099a8057"},
		{"pre_fix_00000000000000000000000000", "pre_fix", "00000000-0000-0000-0000-000000000000"},
	}
	for _, v := range valid {
		id, err := ParseID(v.id)
		if err != nil {
			t.Errorf("ParseID(%q): %v", v.id, err)
			continue
		}
		if id.Prefix() != v.prefix {
			t.Errorf("%q: prefix %q, want %q", v.id, id.Prefix(), v.prefix)
		}
		u, _ := id.UUID()
		if u.String() != v.uuid {
			t.Errorf("%q: uuid %s, want %s", v.id, u, v.uuid)
		}
		if got := FromUUID(v.prefix, uuid.MustParse(v.uuid)); string(got) != v.id {
			t.Errorf("FromUUID(%q, %s) = %q, want %q", v.prefix, v.uuid, got, v.id)
		}
	}

	invalid := []string{
		"PREFIX_00000000000000000000000000",
		"12345_00000000000000000000000000",
		"pre.fix_00000000000000000000000000",
		"préfix_00000000000000000000000000",
		"  prefix_00000000000000000000000000",
		strings.Repeat("a", 64) + "_00000000000000000000000000",
		"_00000000000000000000000000",
		"_",
		"prefix_1234567890123456789012345",
		"prefix_123456789012345678901234567",
		"prefix_1234567890123456789012345 ",
		"prefix_0123456789ABCDEFGHJKMNPQRS",
		"prefix_123456789-123456789-123456",
		"prefix_ooooooiiiiiiuuuuuuulllllll",
		"prefix_i23456789ol23456789oi23456",
		"prefix_123456789-0123456789-0123456",
		"prefix_8zzzzzzzzzzzzzzzzzzzzzzzzz",
		"_prefix_00000000000000000000000000",
		"prefix__00000000000000000000000000",
		"",
		"prefix_",
	}
	for _, s := range invalid {
		if _, err := ParseID(s); err == nil {
			t.Errorf("ParseID(%q) should fail", s)
		}
	}
}

func TestNewIDRoundTripAndOrder(t *testing.T) {
	before := time.Now().Add(-time.Second)
	prev := NewID("site")
	for i := 0; i < 1000; i++ {
		id := NewID("site")
		if !id.Valid() || id.Prefix() != "site" {
			t.Fatalf("bad id %q", id)
		}
		u, err := id.UUID()
		if err != nil {
			t.Fatal(err)
		}
		if FromUUID("site", u) != id {
			t.Fatalf("round trip changed %q", id)
		}
		if id <= prev {
			t.Fatalf("ids should increase: %q after %q", id, prev)
		}
		prev = id
	}
	if ts := prev.Time(); ts.Before(before) || ts.After(time.Now().Add(time.Second)) {
		t.Errorf("Time() = %v", ts)
	}
}

func TestValidPrefix(t *testing.T) {
	for _, p := range []string{"site", "mbox", "pre_fix", "a"} {
		if !ValidPrefix(p) {
			t.Errorf("ValidPrefix(%q) = false", p)
		}
	}
	for _, p := range []string{"", "Site", "_site", "site_", "s1te", "si-te"} {
		if ValidPrefix(p) {
			t.Errorf("ValidPrefix(%q) = true", p)
		}
	}
}

func FuzzParseID(f *testing.F) {
	f.Add("site_01h455vb4pex5vsknk084sn02q")
	f.Add("prefix_8zzzzzzzzzzzzzzzzzzzzzzzzz")
	f.Add("_")
	f.Fuzz(func(t *testing.T, s string) {
		id, err := ParseID(s)
		if err != nil {
			return
		}
		u, err := id.UUID()
		if err != nil {
			t.Fatalf("valid id %q has no uuid: %v", s, err)
		}
		if got := FromUUID(id.Prefix(), u); string(got) != s {
			t.Fatalf("round trip %q -> %q", s, got)
		}
	})
}
