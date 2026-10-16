package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
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
		{"kind without the resource widget", func(i *KindInfo) {
			i.UI.Fields = map[string]FieldHint{"database": {Kind: "databases.database"}}
		}, "doesn't show resources"},
		{"resource field naming no kind", func(i *KindInfo) {
			i.UI.Fields = map[string]FieldHint{"database": {Widget: "resource", Kind: "Databases"}}
		}, "isn't a kind's name"},
		{"unknown level", func(i *KindInfo) { i.Levels = []ScopeLevel{"galaxy"} }, "scope level"},
		{"level twice", func(i *KindInfo) { i.Levels = []ScopeLevel{ScopeAccount, ScopeAccount} }, "twice"},
		{"level of the scope", func(i *KindInfo) { i.Levels = []ScopeLevel{ScopeServer} }, "twice"},
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
	// A resource field may name its kind, for forms to list its resources,
	// or leave it out and take an ID.
	for _, hint := range []FieldHint{{Widget: "resource", Kind: "databases.database"}, {Widget: "resource"}} {
		info := good
		info.UI.Fields = map[string]FieldHint{"database": hint}
		if err := Define[testSiteSpec, Status](info).Err(); err != nil {
			t.Errorf("a resource field %+v should be accepted: %v", hint, err)
		}
	}
	type noStatus struct{ X int }
	if err := Define[testSiteSpec, noStatus](good).Err(); err == nil {
		t.Error("a status without store.Status should be rejected")
	}
}

// A kind with several levels has resources wherever they're made, and
// nowhere else.
func TestKindsOwnedAtSeveralLevels(t *testing.T) {
	ctx := context.Background()
	k := Define[testSiteSpec, Status](KindInfo{
		Name: "test.member", Prefix: "tmember", Scope: ScopeServer, Levels: []ScopeLevel{ScopeAccount},
		Title: "Member", Plural: "members", Description: "Someone, somewhere.",
	})
	if err := k.Err(); err != nil {
		t.Fatal(err)
	}
	info := k.Info()
	if !info.OwnedAt(ScopeServer) || !info.OwnedAt(ScopeAccount) || info.OwnedAt(ScopeReseller) {
		t.Errorf("levels = %s and %v", info.Scope, info.Levels)
	}
	kinds := NewKindRegistry()
	if err := kinds.Register(k); err != nil {
		t.Fatal(err)
	}
	st := NewMemory(kinds)
	err := st.Write(ctx, WriteMeta{}, func(tx Tx) error {
		for _, scope := range []Scope{ServerScope(), AccountScope(NewID("acct"))} {
			if err := tx.Create(ctx, &Resource{Meta: Meta{Kind: "test.member", Scope: scope}, Spec: json.RawMessage(`{"domains":["a.test"]}`)}); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &Resource{Meta: Meta{Kind: "test.member", Scope: ResellerScope(NewID("rsl"))}, Spec: json.RawMessage(`{"domains":["a.test"]}`)}
	if err := k.Validate(ctx, r, nil); !i18n.ErrInvalid.Is(err) || !strings.Contains(i18n.From(err).Fields[0].Message, "server or account level") {
		t.Errorf("a reseller's member = %v", err)
	}
	err = st.Write(ctx, WriteMeta{}, func(tx Tx) error { return tx.Create(ctx, r) })
	if !i18n.ErrInvalid.Is(err) {
		t.Errorf("storing a reseller's member = %v", err)
	}
}

func TestReadersInContexts(t *testing.T) {
	ctx := context.Background()
	if _, ok := ReaderFrom(ctx); ok {
		t.Error("a plain context carries no reader")
	}
	if _, ok := ReaderFrom(WithReader(ctx, nil)); ok {
		t.Error("a nil reader isn't carried")
	}
	st := NewMemory(NewKindRegistry())
	if r, ok := ReaderFrom(WithReader(ctx, st)); !ok || r != Reader(st) {
		t.Errorf("reader = %v, %v", r, ok)
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

// The API serves every kind's operations at a path and under IDs of its own,
// so the registry refuses a kind that would share them with another, rather
// than the API's router stopping the binary at startup.
func TestKindsKeepTheirPathsAndOperations(t *testing.T) {
	kind := func(name, prefix, plural string, actions ...Action) Kind {
		return Define[testSiteSpec, Status](KindInfo{Name: name, Prefix: prefix, Scope: ScopeServer, Title: "Thing", Plural: plural, Description: "A thing.", Actions: actions})
	}
	site := kind("test.site", "tsite", "sites", Action{Name: "deploy", Summary: "Deploy it.", Risk: RiskLow})
	if got := site.Info().OperationIDs(); !slices.Equal(got, []string{"listTestSites", "getTestSite", "createTestSite", "updateTestSite", "deleteTestSite", "getTestSiteHistory", "deployTestSite"}) {
		t.Errorf("operation IDs = %v", got)
	}
	if site.Info().Path() != "test/sites" || kind("mail.mailbox_alias", "malias", "mailbox-aliases").Info().TypeName() != "MailMailboxAlias" {
		t.Errorf("names = %s, %s", site.Info().Path(), kind("mail.mailbox_alias", "malias", "mailbox-aliases").Info().TypeName())
	}
	page := kind("test.page", "tpage", "pages", Action{Name: "get_full", Summary: "Get all of it.", Risk: RiskLow})
	for _, tc := range []struct {
		name          string
		first, second Kind
	}{
		// Both would be served at /api/v1/test/sites.
		{"the same plural", site, kind("test.place", "tplace", "sites")},
		// getTestSiteHistory is test.site's history.
		{"a get that is another kind's history", site, kind("test.site_history", "thist", "site-histories")},
		// getFullTestPage is test.page's get_full action.
		{"a get that is another kind's action", page, kind("full.test_page", "fpage", "test-pages")},
	} {
		reg := NewKindRegistry()
		if err := reg.Register(tc.first); err != nil {
			t.Fatal(err)
		}
		if err := reg.Register(tc.second); err == nil {
			t.Errorf("%s: %s was registered beside %s", tc.name, tc.second.Info().Name, tc.first.Info().Name)
		}
		if _, ok := reg.Get(tc.second.Info().Name); ok {
			t.Errorf("%s: %s is in the registry", tc.name, tc.second.Info().Name)
		}
	}
	// A kind that was refused leaves nothing behind, so another can take
	// what it would have had.
	reg := NewKindRegistry()
	if err := reg.Register(site); err != nil {
		t.Fatal(err)
	}
	if err := reg.Register(kind("test.place", "tplace", "sites")); err == nil {
		t.Fatal("the same plural")
	}
	if err := reg.Register(kind("test.place", "tplace", "places")); err != nil {
		t.Errorf("after a refusal: %v", err)
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
		"submit":         "web.site.submitted",
		"commit":         "web.site.committed",
		"resubmit":       "web.site.resubmitted",
		"transfer":       "web.site.transferred",
		"spin_down":      "web.site.spun_down",
		"spin_up":        "web.site.spun_up",
		"spin_down_all":  "web.site.all_spun_down",
		"power_off":      "web.site.powered_off",
		"log_out":        "web.site.logged_out",
		"back_up":        "web.site.backed_up",
		"roll_back":      "web.site.rolled_back",
		"rerun":          "web.site.reran",
		"rebuild":        "web.site.rebuilt",
		"undo":           "web.site.undone",
		"unset":          "web.site.unset",
		"unfreeze":       "web.site.unfrozen",
		"rescan":         "web.site.rescanned",
		"unpin":          "web.site.unpinned",
		"unlock":         "web.site.unlocked",
		"render":         "web.site.rendered",
		"restart_all":    "web.site.all_restarted",
		"retry":          "web.site.retried",
		"resync":         "web.site.resynced",
		"rename":         "web.site.renamed",
		"redeploy":       "web.site.redeployed",
		"rekey":          "web.site.rekeyed",
		"ping":           "web.site.pinged",
		"edit":           "web.site.edited",
		"visit":          "web.site.visited",
		"cancel":         "web.site.canceled",
		"label":          "web.site.labeled",
		"wake":           "web.site.woken",
		"send":           "web.site.sent",
		"resend":         "web.site.resent",
	}
	for action, want := range tests {
		if got := (Action{Name: action}).EventName("web.site"); got != want {
			t.Errorf("EventName(%q) = %q, want %q", action, got, want)
		}
	}
	if got := (Action{Name: "repay", Past: "repaid"}).EventName("web.site"); got != "web.site.repaid" {
		t.Errorf("explicit past tense ignored: %q", got)
	}
	bad := Define[testSiteSpec, testSiteStatus](KindInfo{
		Name: "test.thing", Prefix: "thing", Scope: ScopeAccount, Title: "Thing", Plural: "things", Description: "A thing.",
		Actions: []Action{{Name: "repay", Summary: "Repay.", Risk: RiskLow, Past: "Re-paid"}},
	})
	if bad.Err() == nil || !strings.Contains(bad.Err().Error(), "past tense") {
		t.Errorf("a malformed past tense is refused: %v", bad.Err())
	}
}

func TestGetChecksTheKind(t *testing.T) {
	ctx := context.Background()
	s, k := newTestStore(t)
	r := createSite(t, s, k, NewID("acct"), "kind.test")
	other := Define[testSiteSpec, testSiteStatus](KindInfo{Name: "test.other", Prefix: "other", Scope: ScopeAccount, Title: "Other", Plural: "others", Description: "Another kind."})
	if _, err := other.Get(ctx, s, r.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("a resource of another kind isn't found: %v", err)
	}
	if got, err := k.Get(ctx, s, r.ID); err != nil || got.ID != r.ID {
		t.Errorf("get = %v, %v", got, err)
	}
}

func TestSensitiveFieldPaths(t *testing.T) {
	define := func(path string) error {
		return Define[testSiteSpec, testSiteStatus](KindInfo{
			Name: "test.thing", Prefix: "thing", Scope: ScopeAccount, Title: "Thing", Plural: "things", Description: "A thing.",
			UI: UIHints{Fields: map[string]FieldHint{path: {Sensitive: true}, "notes": {Widget: "textarea"}}},
		}).Err()
	}
	for _, ok := range []string{"password", "smtp.authPassword", "db.user.password"} {
		if err := define(ok); err != nil {
			t.Errorf("%s: %v", ok, err)
		}
	}
	for _, bad := range []string{"users[].password", "Password", "db..password", "a." + strings.Repeat("x", 48)} {
		if err := define(bad); err == nil {
			t.Errorf("%s should be refused", bad)
		}
	}
	info := &KindInfo{UI: UIHints{Fields: map[string]FieldHint{"b": {Sensitive: true}, "a": {Sensitive: true}, "c": {}}}}
	if got := SensitiveFields(info); len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Errorf("sensitive fields = %v", got)
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

type strictSpec struct {
	Name    string            `json:"name" minLength:"1" maxLength:"20"`
	Mode    string            `json:"mode,omitempty" enum:"fast,slow"`
	Workers int               `json:"workers,omitempty" minimum:"1" maximum:"8"`
	Tags    []string          `json:"tags,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
}

func TestSpecsAreValidatedAgainstTheirSchema(t *testing.T) {
	k := Define[strictSpec, Status](KindInfo{
		Name: "test.strict", Prefix: "strict", Scope: ScopeServer, Title: "Strict", Plural: "stricts", Description: "A kind with a strict schema.",
	})
	if err := k.Err(); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	check := func(spec string) []i18n.FieldError {
		t.Helper()
		err := k.Validate(ctx, &Resource{Meta: Meta{Kind: "test.strict", Scope: ServerScope()}, Spec: json.RawMessage(spec)}, nil)
		if err == nil {
			return nil
		}
		var e *i18n.Error
		if !errors.As(err, &e) || e.Code != i18n.ErrInvalid.Code {
			t.Fatalf("%s: %v", spec, err)
		}
		return e.Fields
	}
	if fields := check(`{"name":"web","mode":"fast","workers":4,"tags":["a"],"env":{"A":"1"}}`); fields != nil {
		t.Errorf("a valid spec: %+v", fields)
	}
	for spec, want := range map[string]string{
		`{}`:                            "spec.name",
		`{"name":""}`:                   "spec.name",
		`{"name":"web","mode":"warp"}`:  "spec.mode",
		`{"name":"web","workers":9}`:    "spec.workers",
		`{"name":"web","workers":"4"}`:  "spec.workers",
		`{"name":"web","colour":"red"}`: "spec.colour",
		`{"name":"web","tags":[1]}`:     "spec.tags[0]",
	} {
		fields := check(spec)
		if len(fields) == 0 {
			t.Errorf("%s should be refused", spec)
			continue
		}
		if fields[0].Path != want {
			t.Errorf("%s: problem at %s, want %s (%+v)", spec, fields[0].Path, want, fields)
		}
	}
	if fields := check(`{}`); fields[0].Code != i18n.ErrRequired.Code {
		t.Errorf("a missing field is reported as required: %+v", fields)
	}
	if fields := check(`{"name": "web"`); len(fields) != 1 || fields[0].Path != "spec" {
		t.Errorf("a spec that isn't JSON: %+v", fields)
	}
}

type resizeInput struct {
	Size  int    `json:"size" minimum:"1"`
	Grow  bool   `json:"grow,omitempty"`
	Label string `json:"label,omitempty"`
}

func (in resizeInput) Validate() error {
	if in.Label == "forbidden" {
		return errors.New("that label is taken")
	}
	return nil
}

func TestActionInputs(t *testing.T) {
	a := Action{Name: "resize", Summary: "Resize.", Risk: RiskMedium, Input: resizeInput{}}
	for input, ok := range map[string]bool{
		`{"size":2}`:                     true,
		`{"size":2,"grow":true}`:         true,
		`{"size":0}`:                     false,
		`{}`:                             false,
		`{"size":2,"shrink":true}`:       false,
		`{"size":"2"}`:                   false,
		`{"size":2,"label":"forbidden"}`: false,
	} {
		err := ValidateActionInput(a, json.RawMessage(input))
		if (err == nil) != ok {
			t.Errorf("%s: %v", input, err)
		}
		if err != nil && !errors.Is(err, i18n.ErrInvalid) {
			t.Errorf("%s: problems with inputs are invalid values: %v", input, err)
		}
	}
	none := Action{Name: "touch", Summary: "Touch.", Risk: RiskLow}
	for _, input := range []string{``, `{}`, `null`} {
		if err := ValidateActionInput(none, json.RawMessage(input)); err != nil {
			t.Errorf("%q for an action without input: %v", input, err)
		}
	}
	if err := ValidateActionInput(none, json.RawMessage(`{"x":1}`)); err == nil {
		t.Error("an action without input refuses one")
	}
}

func TestKindRulesForIndexesAndEvents(t *testing.T) {
	define := func(info KindInfo) error {
		info.Name, info.Prefix, info.Scope, info.Title, info.Plural, info.Description = "test.thing", "thing", ScopeServer, "Thing", "things", "A thing."
		return Define[testSiteSpec, Status](info).Err()
	}
	for name, info := range map[string]KindInfo{
		"index name":         {Indexes: []Index{{Name: "Domain", Path: "domains"}}},
		"index with a dash":  {Indexes: []Index{{Name: "do-main", Path: "domains"}}},
		"reserved index":     {Indexes: []Index{{Name: "sort", Path: "domains"}}},
		"reserved action":    {Actions: []Action{{Name: "history", Summary: "x", Risk: RiskLow}}},
		"generated event":    {Actions: []Action{{Name: "make", Summary: "x", Risk: RiskLow, Past: "created"}}},
		"same event twice":   {Actions: []Action{{Name: "stop", Summary: "x", Risk: RiskLow}, {Name: "halt", Summary: "x", Risk: RiskLow, Past: "stopped"}}},
		"input not a struct": {Actions: []Action{{Name: "go", Summary: "x", Risk: RiskLow, Input: "fast"}}},
	} {
		if err := define(info); err == nil {
			t.Errorf("%s should be refused", name)
		}
	}
	if err := define(KindInfo{Indexes: []Index{{Name: "domainName", Path: "domains"}}}); err != nil {
		t.Errorf("a camelCase index name: %v", err)
	}
}
