package store

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
)

type testSiteSpec struct {
	Domains []string `json:"domains"`
	Runtime string   `json:"runtime,omitempty"`
}

type testSiteStatus struct {
	Status
	URL string `json:"url,omitempty"`
}

var errTestDomainTaken = i18n.Define("test.domain_taken", http.StatusConflict, "The domain {value} is already used by another site.")

func testSiteKind() *KindOf[testSiteSpec, testSiteStatus] {
	return Define[testSiteSpec, testSiteStatus](KindInfo{
		Name:        "test.site",
		Prefix:      "tsite",
		Scope:       ScopeAccount,
		Title:       "Site",
		Plural:      "sites",
		Description: "A test site.",
		Indexes: []Index{
			{Name: "domain", Path: "domains", Unique: true, Fold: true, Conflict: errTestDomainTaken},
			{Name: "runtime", Path: "runtime"},
		},
		Actions: []Action{
			{Name: "deploy", Summary: "Deploy a release.", Risk: RiskMedium},
			{Name: "stop", Summary: "Stop the site.", Risk: RiskMedium},
			{Name: "reset_password", Summary: "Reset the password.", Risk: RiskHigh},
		},
	}).WithDefaults(func(ctx context.Context, obj *Object[testSiteSpec, testSiteStatus]) error {
		if obj.Spec.Runtime == "" {
			obj.Spec.Runtime = "static"
		}
		return nil
	}).WithValidator(func(ctx context.Context, obj, old *Object[testSiteSpec, testSiteStatus]) error {
		if len(obj.Spec.Domains) == 0 {
			return i18n.Invalid(i18n.Field("spec.domains", i18n.ErrRequired))
		}
		return nil
	})
}

func TestDefineValid(t *testing.T) {
	k := testSiteKind()
	if err := k.Err(); err != nil {
		t.Fatal(err)
	}
	info := k.Info()
	if info.Module() != "test" {
		t.Errorf("Module() = %q", info.Module())
	}
	if info.Risks.Delete != RiskHigh || info.Risks.Create != RiskLow {
		t.Errorf("default risks = %+v", info.Risks)
	}
	if _, ok := info.Action("deploy"); !ok {
		t.Error("deploy action not found")
	}
}

func TestDefineRejectsBadDefinitions(t *testing.T) {
	good := KindInfo{Name: "test.thing", Prefix: "thing", Scope: ScopeServer, Title: "Thing", Plural: "things", Description: "A thing."}
	tests := []struct {
		name   string
		mutate func(*KindInfo)
		want   string
	}{
		{"name", func(i *KindInfo) { i.Name = "Thing" }, "name must be"},
		{"prefix", func(i *KindInfo) { i.Prefix = "Thing1" }, "prefix"},
		{"scope", func(i *KindInfo) { i.Scope = "galaxy" }, "scope level"},
		{"plural", func(i *KindInfo) { i.Plural = "Things" }, "plural"},
		{"description", func(i *KindInfo) { i.Description = "" }, "description"},
		{"reserved action", func(i *KindInfo) { i.Actions = []Action{{Name: "delete", Summary: "x", Risk: RiskLow}} }, "reserved"},
		{"action risk", func(i *KindInfo) { i.Actions = []Action{{Name: "go", Summary: "x"}} }, "risk"},
		{"duplicate action", func(i *KindInfo) {
			i.Actions = []Action{{Name: "go", Summary: "x", Risk: RiskLow}, {Name: "go", Summary: "x", Risk: RiskLow}}
		}, "twice"},
		{"backup reason", func(i *KindInfo) { i.Backup = Backup{Exclude: true} }, "reason"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := good
			tt.mutate(&info)
			err := Define[testSiteSpec, Status](info).Err()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want mention of %q", err, tt.want)
			}
		})
	}
	if err := Define[testSiteSpec, Status](good).Err(); err != nil {
		t.Errorf("plain Status should be accepted: %v", err)
	}
	type noStatus struct{ X int }
	if err := Define[testSiteSpec, noStatus](good).Err(); err == nil {
		t.Error("a status without store.Status should be rejected")
	}
}

func TestRegistry(t *testing.T) {
	reg := NewKindRegistry()
	k := testSiteKind()
	if err := reg.Register(k); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(k); err == nil {
		t.Error("registering twice should fail")
	}
	if err := reg.Reserve("tsite", "jobs"); err == nil {
		t.Error("reserving a used prefix should fail")
	}
	if err := reg.Reserve("chg", "change sets"); err != nil {
		t.Fatal(err)
	}
	other := Define[testSiteSpec, Status](KindInfo{Name: "test.other", Prefix: "chg", Scope: ScopeServer, Title: "Other", Plural: "others", Description: "Other."})
	if err := reg.Register(other); err == nil {
		t.Error("a reserved prefix should not be usable by a kind")
	}
	got, ok := reg.ForID(NewID("tsite"))
	if !ok || got.Info().Name != "test.site" {
		t.Errorf("ForID = %v, %v", got, ok)
	}
	if n := len(reg.All()); n != 1 {
		t.Errorf("All() has %d kinds", n)
	}
}

func TestKindValidateAndDefaults(t *testing.T) {
	k := testSiteKind()
	ctx := context.Background()
	obj := k.New(AccountScope(NewID("acct")), testSiteSpec{Domains: []string{"example.com"}})
	r, err := k.Encode(obj)
	if err != nil {
		t.Fatal(err)
	}
	if err := k.Default(ctx, r); err != nil {
		t.Fatal(err)
	}
	decoded, err := k.Decode(r)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Spec.Runtime != "static" {
		t.Errorf("default runtime = %q", decoded.Spec.Runtime)
	}
	if err := k.Validate(ctx, r, nil); err != nil {
		t.Errorf("valid resource rejected: %v", err)
	}

	empty, _ := k.Encode(k.New(AccountScope(NewID("acct")), testSiteSpec{}))
	err = k.Validate(ctx, empty, nil)
	var e *i18n.Error
	if !errors.As(err, &e) || len(e.Fields) != 1 || e.Fields[0].Path != "spec.domains" {
		t.Errorf("expected a field error on spec.domains, got %v", err)
	}

	wrongScope, _ := k.Encode(k.New(ServerScope(), testSiteSpec{Domains: []string{"a.test"}}))
	if err := k.Validate(ctx, wrongScope, nil); !errors.Is(err, i18n.ErrInvalid) {
		t.Errorf("wrong scope level should be invalid, got %v", err)
	}
}

func TestEventNames(t *testing.T) {
	tests := map[string]string{
		"deploy":         "web.site.deployed",
		"restart":        "web.site.restarted",
		"stop":           "web.site.stopped",
		"scrub":          "web.site.scrubbed",
		"apply":          "web.site.applied",
		"suspend":        "web.site.suspended",
		"open":           "web.site.opened",
		"sync":           "web.site.synced",
		"run":            "web.site.ran",
		"reset_password": "web.site.password_reset",
		"rotate_keys":    "web.site.keys_rotated",
	}
	for action, want := range tests {
		if got := (Action{Name: action}).EventName("web.site"); got != want {
			t.Errorf("EventName(%q) = %q, want %q", action, got, want)
		}
	}
	if got := (Action{Name: "roll_back", Past: "rolled_back"}).EventName("web.site"); got != "web.site.rolled_back" {
		t.Errorf("explicit past tense ignored: %q", got)
	}
}

func TestStatusConditions(t *testing.T) {
	var s Status
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if !s.SetCondition(Condition{Type: "Ready", Status: ConditionFalse, Reason: "Starting"}, t0) {
		t.Fatal("adding a condition should report a change")
	}
	if s.SetCondition(Condition{Type: "Ready", Status: ConditionFalse, Reason: "Starting"}, t0.Add(time.Minute)) {
		t.Error("setting the same condition should not report a change")
	}
	t1 := t0.Add(2 * time.Minute)
	s.SetCondition(Condition{Type: "Ready", Status: ConditionTrue, Reason: "Running"}, t1)
	c, _ := s.Condition("Ready")
	if !c.LastTransitionTime.Equal(t1) || !s.IsTrue("Ready") {
		t.Errorf("condition = %+v", c)
	}
	s.SetCondition(Condition{Type: "Ready", Status: ConditionTrue, Reason: "StillRunning"}, t1.Add(time.Hour))
	c, _ = s.Condition("Ready")
	if !c.LastTransitionTime.Equal(t1) {
		t.Error("the transition time should only move when the status changes")
	}
}

func TestValidateLabels(t *testing.T) {
	if err := ValidateLabels(map[string]string{"env": "prod", "racksail.dev/owner": "web"}); err != nil {
		t.Error(err)
	}
	if err := ValidateLabels(map[string]string{"Env": "x"}); err == nil {
		t.Error("uppercase label keys should be rejected")
	}
	if err := ValidateLabels(map[string]string{"k": strings.Repeat("v", 256)}); err == nil {
		t.Error("long label values should be rejected")
	}
}
