package updates

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/events"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// Window is when automatic updates may be installed, every day.
type Window struct {
	Start string `json:"start" pattern:"^([01][0-9]|2[0-3]):[0-5][0-9]$" doc:"When the window opens, as HH:MM" example:"03:00"`
	End   string `json:"end" pattern:"^([01][0-9]|2[0-3]):[0-5][0-9]$" doc:"When it closes; earlier than the start means the next day" example:"05:00"`
	// TimeZone is an IANA name; the server's, UTC, when empty.
	TimeZone string `json:"timeZone,omitempty" maxLength:"64" doc:"The time zone the times are in; UTC when empty"`
}

var clockTime = regexp.MustCompile(`^([01][0-9]|2[0-3]):([0-5][0-9])$`)

func clockMinutes(s string) int {
	m := clockTime.FindStringSubmatch(s)
	if m == nil {
		return -1
	}
	var h, mm int
	fmt.Sscanf(m[1]+" "+m[2], "%d %d", &h, &mm)
	return h*60 + mm
}

// Open reports whether t falls in the window.
func (w *Window) Open(t time.Time) bool {
	if w == nil {
		return true
	}
	loc := time.UTC
	if w.TimeZone != "" {
		if l, err := time.LoadLocation(w.TimeZone); err == nil {
			loc = l
		}
	}
	t = t.In(loc)
	start, end := clockMinutes(w.Start), clockMinutes(w.End)
	m := t.Hour()*60 + t.Minute()
	if start < 0 || end < 0 || start == end {
		return false
	}
	if start < end {
		return m >= start && m < end
	}
	return m >= start || m < end
}

// SettingsSpec is how the server updates Racksail.
type SettingsSpec struct {
	Channel Channel `json:"channel" enum:"stable,beta,nightly" doc:"Which releases the server takes: stable, beta and stable, or every nightly build too"`
	// Automatic installs new releases by themselves, in the window.
	Automatic bool    `json:"automatic,omitempty" doc:"Install new releases on the channel by themselves, during the window"`
	Window    *Window `json:"window,omitempty" doc:"When automatic updates may be installed; any time when empty"`
	// Mirrors serve releases, and Sigstore's trusted root, when GitHub
	// can't be reached; they're tried first.
	Mirrors []string `json:"mirrors,omitempty" maxItems:"8" doc:"Mirrors to download releases from, tried before GitHub" example:"[\"https://mirror.example.com/racksail\"]"`
}

// Available is a release the server could update to.
type Available struct {
	Tag       string    `json:"tag"`
	Version   string    `json:"version"`
	Channel   Channel   `json:"channel" enum:"stable,beta,nightly"`
	Published time.Time `json:"published"`
	NotesURL  string    `json:"notesUrl,omitempty" doc:"The release notes"`
	// Asset is the binary for this server's architecture, as the signed
	// manifest lists it.
	Asset Asset `json:"asset"`
}

// OSStatus is the server's Debian release and the next one.
type OSStatus struct {
	Codename string `json:"codename"`
	Version  string `json:"version"`
	// Next is the release an upgrade would move to, when there is one.
	Next           string `json:"next,omitempty" doc:"The Debian release an upgrade would move to"`
	RebootRequired bool   `json:"rebootRequired,omitempty"`
}

// SettingsStatus is what the updater last found.
type SettingsStatus struct {
	store.Status
	Running   string     `json:"running" doc:"The version running now"`
	Available *Available `json:"available,omitempty" doc:"The newest release on the channel, when it is newer"`
	CheckedAt *time.Time `json:"checkedAt,omitempty"`
	// CheckError says why the last check failed.
	CheckError  string       `json:"checkError,omitempty"`
	Last        *UpdateState `json:"last,omitempty" doc:"The last update the agent installed"`
	CanRollBack bool         `json:"canRollBack,omitempty"`
	OS          *OSStatus    `json:"os,omitempty"`
	// Reported is the last update whose outcome was published, so a
	// restart doesn't publish it again.
	Reported string `json:"reported,omitempty"`
}

// CheckOutput is what a check found.
type CheckOutput struct {
	Running   string     `json:"running"`
	Available *Available `json:"available,omitempty" doc:"The release to update to; empty when the server is up to date"`
}

// UpdateInput names the release to install.
type UpdateInput struct {
	// Version must be the one the last check found, so a release that
	// appeared since isn't installed unseen.
	Version string `json:"version,omitempty" doc:"The version the last check found; empty takes it"`
}

// Validate implements the check every action input gets.
func (in UpdateInput) Validate() error {
	if in.Version != "" {
		if _, err := ParseVersion(in.Version); err != nil {
			return i18n.Invalid(i18n.Field("input.version", i18n.ErrBadValue, "reason", err.Error()))
		}
	}
	return nil
}

// UpdateOutput is what installing a release did.
type UpdateOutput struct {
	From string `json:"from"`
	To   string `json:"to"`
	// RestartsAt is when Racksail restarts onto the new release.
	RestartsAt time.Time `json:"restartsAt"`
}

// RollbackInput says why.
type RollbackInput struct {
	Reason string `json:"reason,omitempty" maxLength:"500"`
}

// Validate implements the check every action input gets.
func (in RollbackInput) Validate() error { return nil }

// UpgradeOSInput names the Debian release to move to.
type UpgradeOSInput struct {
	To string `json:"to" doc:"The code name of the next Debian release, such as forky"`
}

// Validate implements the check every action input gets.
func (in UpgradeOSInput) Validate() error {
	if _, ok := debianVersions[in.To]; !ok {
		return i18n.Invalid(i18n.Field("input.to", i18n.ErrBadValue, "reason", "not a Debian release Racksail knows"))
	}
	return nil
}

// UpgradeOSOutput is what the upgrade did.
type UpgradeOSOutput struct {
	From           string   `json:"from"`
	To             string   `json:"to"`
	Snapshots      []string `json:"snapshots,omitempty" doc:"The snapshots taken first"`
	RebootRequired bool     `json:"rebootRequired" doc:"Whether the server needs a reboot to finish"`
}

// Errors.
var (
	ErrNothingToInstall = i18n.Define("updates.nothing_to_install", http.StatusConflict, "There is no newer release to install. Check for updates first.")
	ErrStaleCheck       = i18n.Define("updates.stale_check", http.StatusConflict, "The last check found {found}, not {version}. Check for updates again.")
	ErrDevBuild         = i18n.Define("updates.dev_build", http.StatusConflict, "This is a development build, which doesn't update itself.")
	ErrOSUpgrade        = i18n.Define("updates.os_upgrade", http.StatusConflict, "Debian can't be upgraded from {from} to {to}: {reason}.")
	ErrUpdateFailed     = i18n.Define("updates.failed", http.StatusBadGateway, "The update couldn't be installed: {reason}.")
)

// Kind is core.update_settings.
var Kind = store.Define[SettingsSpec, SettingsStatus](store.KindInfo{
	Name: "core.update_settings", Prefix: "upd", Scope: store.ScopeServer, Singleton: true,
	Title: "Update settings", Plural: "update-settings",
	Description: "How the server updates Racksail: the release channel, whether new releases install themselves and when, and mirrors. " +
		"Releases are signed by Racksail's release workflow and checked before anything is installed; the system is snapshotted first, " +
		"and a release that doesn't come up healthy is rolled back by itself.",
	Actions: []store.Action{
		{
			Name: "check", Title: "Check for updates", Summary: "Look for a newer release on the channel",
			Description: "Lists the releases on the channel, and fetches and checks the signed manifest of the newest one that is newer than the running version.",
			Risk:        store.RiskLow, Output: CheckOutput{},
		},
		{
			Name: "install", Title: "Install update", Summary: "Install the release the last check found",
			Description: "Backs up the panel's database, has the agent download the release and check its signature, snapshot the system and put the " +
				"new binary in place, then restarts Racksail onto it. If the new release doesn't report healthy within fifteen minutes, " +
				"the previous one comes back by itself.",
			Risk: store.RiskHigh, Input: UpdateInput{}, Output: UpdateOutput{}, Long: true,
		},
		{
			Name: "rollback", Title: "Roll back", Summary: "Go back to the release before the last update",
			Description: "Puts back the binary and the panel's database from before the last update and restarts Racksail. " +
				"Changes made since the update are lost.",
			Risk: store.RiskHigh, Input: RollbackInput{}, Destructive: true,
		},
		{
			Name: "upgrade_os", Title: "Upgrade Debian", Summary: "Upgrade Debian to its next major release",
			Description: "Asks every module whether the upgrade can go ahead, snapshots the system, points apt at the next release and upgrades " +
				"every package. Modules prepare first and check their services afterwards. The server needs a reboot to finish.",
			Risk: store.RiskCritical, Input: UpgradeOSInput{}, Output: UpgradeOSOutput{}, Long: true, Destructive: true,
		},
	},
	Risks: store.Risks{Create: store.RiskMedium, Update: store.RiskMedium, Delete: store.RiskMedium},
	UI: store.UIHints{
		Icon: "refresh-cw", TitleField: "channel", Hidden: true,
		Fields: map[string]store.FieldHint{"channel": {Order: 0}, "automatic": {Order: 1}, "window": {Order: 2}, "mirrors": {Advanced: true}},
	},
	Backup: store.Backup{Exclude: true, Reason: "Update settings belong to the server, not to what it hosts."},
}).WithValidator(validate)

func validate(ctx context.Context, obj, old *store.Object[SettingsSpec, SettingsStatus]) error {
	var fields []i18n.FieldError
	if !obj.Spec.Channel.Valid() {
		fields = append(fields, i18n.Field("spec.channel", i18n.ErrBadValue, "reason", "use stable, beta or nightly"))
	}
	if w := obj.Spec.Window; w != nil {
		if clockMinutes(w.Start) < 0 || clockMinutes(w.End) < 0 || w.Start == w.End {
			fields = append(fields, i18n.Field("spec.window", i18n.ErrBadValue, "reason", "use two different times such as 03:00 and 05:00"))
		}
		if w.TimeZone != "" {
			if _, err := time.LoadLocation(w.TimeZone); err != nil || w.TimeZone == "Local" {
				fields = append(fields, i18n.Field("spec.window.timeZone", i18n.ErrBadValue, "reason", "use a time zone name such as Europe/Madrid"))
			}
		}
	}
	for i, m := range obj.Spec.Mirrors {
		u, err := url.Parse(m)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
			fields = append(fields, i18n.Field(fmt.Sprintf("spec.mirrors[%d]", i), i18n.ErrBadValue, "reason", "use an https URL"))
		}
	}
	if len(fields) > 0 {
		return i18n.Invalid(fields...)
	}
	return nil
}

// Events the updater publishes.
var (
	EventAvailable  = events.Type{Name: "core.update.available", Description: "A newer Racksail release is available on the server's channel.", Severity: events.SeverityNotice, Data: Available{}}
	EventInstalled  = events.Type{Name: "core.update.installed", Description: "Racksail was updated, and the new release confirmed it works.", Severity: events.SeverityNotice, Data: UpdateState{}}
	EventRolledBack = events.Type{Name: "core.update.rolled_back", Description: "A Racksail update was rolled back to the release before it.", Severity: events.SeverityError, Data: UpdateState{}}
	EventOSUpgraded = events.Type{Name: "core.os_upgrade.finished", Description: "Debian was upgraded to its next major release; the server needs a reboot.", Severity: events.SeverityWarning, Data: UpgradeOSOutput{}}
	EventOSFailed   = events.Type{Name: "core.os_upgrade.failed", Description: "Upgrading Debian failed; apt was pointed back at the release before.", Severity: events.SeverityCritical}
)

func init() {
	store.RegisterKind(Kind)
	for _, t := range []events.Type{EventAvailable, EventInstalled, EventRolledBack, EventOSUpgraded, EventOSFailed} {
		events.RegisterType(t)
	}
}

// debianVersions are the Debian releases Racksail knows, by code name, in
// order.
var debianVersions = map[string]string{"bookworm": "12", "trixie": "13", "forky": "14", "duke": "15"}

var debianOrder = []string{"bookworm", "trixie", "forky", "duke"}

// NextDebian returns the release after a code name, if Racksail knows it.
func NextDebian(codename string) (string, bool) {
	for i, c := range debianOrder {
		if c == codename && i+1 < len(debianOrder) {
			return debianOrder[i+1], true
		}
	}
	return "", false
}
