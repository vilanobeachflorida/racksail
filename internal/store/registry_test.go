package store

import "testing"

// Kinds registered while the panel runs, as add-ons add them, are replaced
// when what adds them is upgraded, keeping their prefix, and removed once
// nothing of them is left, which frees the prefix for another kind.
func TestRegistryReplaceAndUnregister(t *testing.T) {
	reg := NewKindRegistry()
	first := testSiteKind()
	if err := reg.Replace(first); err == nil {
		t.Error("replacing a kind that isn't registered should fail")
	}
	if err := reg.Register(first); err != nil {
		t.Fatal(err)
	}
	second := testSiteKind()
	if err := reg.Replace(second); err != nil {
		t.Fatal(err)
	}
	if got, ok := reg.Get("test.site"); !ok || got != Kind(second) {
		t.Errorf("Get after Replace = %v, %v", got, ok)
	}
	if got, ok := reg.ForID(NewID("tsite")); !ok || got != Kind(second) {
		t.Errorf("ForID after Replace = %v, %v", got, ok)
	}
	moved := Define[testSiteSpec, testSiteStatus](KindInfo{Name: "test.site", Prefix: "tother", Scope: ScopeAccount, Title: "Site", Plural: "sites", Description: "A test site."})
	if err := reg.Replace(moved); err == nil {
		t.Error("a kind's prefix should never change, since its resources' IDs carry it")
	}
	broken := Define[testSiteSpec, testSiteStatus](KindInfo{Name: "test.site", Prefix: "tsite"})
	if err := reg.Replace(broken); err == nil {
		t.Error("a definition with problems should not replace a working one")
	}

	reg.Unregister("test.site")
	if _, ok := reg.Get("test.site"); ok {
		t.Error("an unregistered kind is still found by name")
	}
	if _, ok := reg.ForID(NewID("tsite")); ok {
		t.Error("an unregistered kind is still found by its IDs")
	}
	reg.Unregister("test.site")
	if err := reg.Register(Define[testSiteSpec, Status](KindInfo{Name: "test.note", Prefix: "tsite", Scope: ScopeServer, Title: "Note", Plural: "notes", Description: "A note."})); err != nil {
		t.Errorf("the prefix should be free again: %v", err)
	}
	if err := reg.Reserve("chg", "change sets"); err != nil {
		t.Fatal(err)
	}
	reg.Unregister("test.unknown")
	if err := reg.Reserve("chg", "again"); err == nil {
		t.Error("unregistering an unknown kind freed a reserved prefix")
	}
}
