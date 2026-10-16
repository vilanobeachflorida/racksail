package recipes

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/reconcile"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// OfficialURL is where the official catalog is published: the files of
// the catalog release on GitHub.
const OfficialURL = "https://github.com/vilanobeachflorida/racksail/releases/download/catalog"

// Names of a published catalog's files.
const (
	IndexName     = "index.json"
	SignatureName = "index.json.sig"
)

// Limits on what a catalog serves, as the catalog tool enforces them.
const (
	maxIndex    = 16 << 20
	maxItemFile = 2 << 20
)

//go:embed keys
var officialKeys embed.FS

// ParseKey reads a catalog's public key: its .pub file, or the base64 line
// in it.
func ParseKey(text string) (ed25519.PublicKey, error) {
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(line)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, errors.New("a catalog key is an Ed25519 public key in base64, as in the catalog's .pub file")
		}
		return ed25519.PublicKey(raw), nil
	}
	return nil, errors.New("no key found")
}

// OfficialKeys are the keys panels trust for the official catalog, built
// in from catalog/keys.
func OfficialKeys() []ed25519.PublicKey {
	var out []ed25519.PublicKey
	entries, _ := fs.ReadDir(officialKeys, "keys")
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".pub") {
			continue
		}
		data, err := officialKeys.ReadFile("keys/" + e.Name())
		if err != nil {
			continue
		}
		if k, err := ParseKey(string(data)); err == nil {
			out = append(out, k)
		}
	}
	return out
}

// CatalogSpec is a catalog the panel offers items from.
type CatalogSpec struct {
	Name  string `json:"name" pattern:"^[a-z0-9][a-z0-9-]{1,62}$" doc:"A short name, such as official or acme" example:"acme"`
	Title string `json:"title" minLength:"1" maxLength:"80" doc:"What the catalog is called in the panel" example:"Acme's apps"`
	// URL serves index.json, index.json.sig and the files the index lists.
	URL     string   `json:"url" format:"uri" maxLength:"2048" doc:"Where the catalog's index.json, its signature and its files are" example:"https://catalog.example.com"`
	Mirrors []string `json:"mirrors,omitempty" maxItems:"8" doc:"Other places serving the same files, tried after the URL"`
	// Keys may sign the index: the catalog's .pub file, or its base64 line.
	Keys     []string `json:"keys" minItems:"1" maxItems:"4" doc:"The public keys whose signature on the index is trusted, as in the catalog's .pub file"`
	Disabled bool     `json:"disabled,omitempty" doc:"Stops offering the catalog's items without forgetting it"`
}

// CatalogStatus is what the panel last fetched of a catalog.
type CatalogStatus struct {
	store.Status
	// Official says the catalog is Racksail's own, signed by a key built
	// into the panel.
	Official    bool       `json:"official,omitempty"`
	SyncedAt    *time.Time `json:"syncedAt,omitempty" doc:"When the index was last fetched"`
	Built       *time.Time `json:"built,omitempty" doc:"When the index was built"`
	Items       int        `json:"items,omitempty"`
	IndexSHA256 string     `json:"indexSha256,omitempty"`
	SyncError   string     `json:"syncError,omitempty" doc:"Why the last fetch failed"`
}

// SyncOutput is what fetching a catalog found.
type SyncOutput struct {
	Items int       `json:"items"`
	Built time.Time `json:"built"`
}

// CatalogKind is core.catalog.
var CatalogKind = store.Define[CatalogSpec, CatalogStatus](store.KindInfo{
	Name: "core.catalog", Prefix: "cat", Scope: store.ScopeServer, Title: "Catalog", Plural: "catalogs",
	Description: "A catalog of apps, runtimes, services, stacks, DNS templates, automations, tuning presets, models, themes and add-ons. " +
		"Its index must be signed by one of the keys trusted when the catalog was added, and every file the panel fetches from it " +
		"must match the index. The panel fetches the index daily while the marketplace module is on.",
	Actions: []store.Action{{
		Name: "sync", Title: "Fetch now", Summary: "Fetch the catalog's index now",
		Description: "Fetches the index and its signature, checks the signature and keeps the index, as the daily fetch does.",
		Risk:        store.RiskLow, Output: SyncOutput{},
	}},
	Indexes: []store.Index{{Name: "name", Path: "name", Unique: true}},
	// Trusting a catalog lets it offer software to install.
	Risks: store.Risks{Create: store.RiskHigh, Update: store.RiskHigh, Delete: store.RiskMedium},
	UI: store.UIHints{
		Icon: "library", TitleField: "title", SubtitleField: "url",
		Columns: []store.Column{{Field: "spec.url", Format: "link"}, {Field: "status.items"}, {Field: "status.syncedAt", Format: "date"}, {Field: "status.phase", Format: "status"}},
		Fields:  map[string]store.FieldHint{"name": {Order: 0, CreateOnly: true}, "title": {Order: 1}, "url": {Order: 2}, "keys": {Order: 3, Widget: "textarea"}, "mirrors": {Advanced: true}},
	},
	Backup: store.Backup{Exclude: true, Reason: "Catalogs are fetched again; they belong to the server, not to what it hosts."},
}).WithValidator(validateCatalog)

func validateCatalog(ctx context.Context, obj, old *store.Object[CatalogSpec, CatalogStatus]) error {
	var fields []i18n.FieldError
	for i, raw := range append([]string{obj.Spec.URL}, obj.Spec.Mirrors...) {
		path := "spec.url"
		if i > 0 {
			path = fmt.Sprintf("spec.mirrors[%d]", i-1)
		}
		u, err := url.Parse(raw)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || u.RawQuery != "" {
			fields = append(fields, i18n.Field(path, i18n.ErrBadValue, "reason", "use an http or https URL without a query"))
		}
	}
	for i, k := range obj.Spec.Keys {
		if _, err := ParseKey(k); err != nil {
			fields = append(fields, i18n.Field(fmt.Sprintf("spec.keys[%d]", i), i18n.ErrBadValue, "reason", err.Error()))
		}
	}
	if old != nil && old.Spec.Name != obj.Spec.Name {
		fields = append(fields, i18n.Field("spec.name", i18n.ErrImmutable))
	}
	if len(fields) > 0 {
		return i18n.Invalid(fields...)
	}
	return nil
}

// Errors of catalogs.
var (
	ErrNotSynced     = i18n.Define("recipes.not_synced", http.StatusConflict, "The {catalog} catalog hasn't been fetched yet.")
	ErrNoItem        = i18n.Define("recipes.no_item", http.StatusNotFound, "The {catalog} catalog has no item {item}.")
	ErrSyncFailed    = i18n.Define("recipes.sync_failed", http.StatusBadGateway, "The {catalog} catalog couldn't be fetched: {reason}.")
	ErrOlderIndex    = i18n.Define("recipes.older_index", http.StatusConflict, "The {catalog} catalog served an index older than the one the panel has, so it was refused.")
	ErrCatalogOff    = i18n.Define("recipes.catalog_disabled", http.StatusConflict, "The {catalog} catalog is turned off.")
	ErrFileMismatch  = i18n.Define("recipes.file_mismatch", http.StatusBadGateway, "{file} from the {catalog} catalog doesn't match its index.")
	ErrMarketplaceOn = i18n.Define("recipes.marketplace_off", http.StatusConflict, "Catalogs are only fetched while the marketplace module is on.")
)

// CatalogOptions configure Catalogs.
type CatalogOptions struct {
	Store store.Store
	HTTP  *http.Client
	// Cache keeps indexes and files between restarts.
	Cache string
	// Active says whether catalogs may be fetched: while the marketplace
	// module is on. Nil means always.
	Active func() bool
	// Official are the keys of the official catalog; OfficialKeys when
	// nil.
	Official []ed25519.PublicKey
	Logger   *slog.Logger
	Now      func() time.Time
}

// Catalogs fetches, checks and keeps catalogs.
type Catalogs struct {
	opts CatalogOptions
	mu   sync.RWMutex
	// indexes are the checked indexes, by catalog.
	indexes map[store.ID]*Index
}

// NewCatalogs builds the catalog client.
func NewCatalogs(opts CatalogOptions) *Catalogs {
	if opts.HTTP == nil {
		opts.HTTP = &http.Client{Timeout: 2 * time.Minute}
	}
	if opts.Official == nil {
		opts.Official = OfficialKeys()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Catalogs{opts: opts, indexes: map[store.ID]*Index{}}
}

func (c *Catalogs) active() bool { return c.opts.Active == nil || c.opts.Active() }

// keys parses a catalog's trusted keys.
func keysOf(spec CatalogSpec) []ed25519.PublicKey {
	var out []ed25519.PublicKey
	for _, k := range spec.Keys {
		if key, err := ParseKey(k); err == nil {
			out = append(out, key)
		}
	}
	return out
}

// official reports whether a catalog is the official one: every key it
// trusts is built into the panel.
func (c *Catalogs) official(spec CatalogSpec) bool {
	keys := keysOf(spec)
	if len(keys) == 0 || len(c.opts.Official) == 0 {
		return false
	}
	for _, k := range keys {
		if !slices.ContainsFunc(c.opts.Official, func(o ed25519.PublicKey) bool { return o.Equal(k) }) {
			return false
		}
	}
	return true
}

func bases(spec CatalogSpec) []string {
	out := []string{strings.TrimSuffix(spec.URL, "/")}
	for _, m := range spec.Mirrors {
		out = append(out, strings.TrimSuffix(m, "/"))
	}
	return out
}

// get fetches one URL, refusing more than max bytes.
func (c *Catalogs) get(ctx context.Context, target string, max int64) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.opts.HTTP.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			return nil, fmt.Errorf("%s: %w", target, ue.Err)
		}
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", target, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes", target, max)
	}
	return data, nil
}

func (c *Catalogs) dir(id store.ID) string {
	if c.opts.Cache == "" {
		return ""
	}
	return filepath.Join(c.opts.Cache, string(id))
}

// verify checks an index against every key a catalog trusts.
func verify(data, sig []byte, keys []ed25519.PublicKey) (*Index, error) {
	var last error = errors.New("the catalog trusts no key")
	for _, k := range keys {
		idx, err := VerifyIndex(data, sig, k)
		if err == nil {
			return idx, nil
		}
		last = err
	}
	return nil, last
}

// Sync fetches a catalog's index, from its URL or a mirror, checks its
// signature and keeps it.
func (c *Catalogs) Sync(ctx context.Context, id store.ID) (*Index, error) {
	obj, err := CatalogKind.Get(ctx, c.opts.Store, id)
	if err != nil {
		return nil, err
	}
	if obj.Spec.Disabled {
		return nil, ErrCatalogOff.New("catalog", obj.Spec.Title)
	}
	if !c.active() {
		return nil, ErrMarketplaceOn.New()
	}
	idx, record, err := c.fetch(ctx, obj)
	c.setStatus(ctx, id, record)
	return idx, err
}

// fetch fetches a catalog's index, checks it and keeps it. It returns what
// to record in the catalog's status about how that went.
func (c *Catalogs) fetch(ctx context.Context, obj *store.Object[CatalogSpec, CatalogStatus]) (*Index, func(st *CatalogStatus), error) {
	id := obj.ID
	official := c.official(obj.Spec)
	idx, data, sig, err := c.fetchIndex(ctx, obj.Spec)
	if err == nil {
		if cur, cerr := c.Index(ctx, id); cerr == nil && idx.Built.Before(cur.Built) {
			err = ErrOlderIndex.New("catalog", obj.Spec.Title)
		}
	}
	if err == nil {
		err = c.keep(id, data, sig)
	}
	if err != nil {
		reason := reasonOf(err)
		if i18n.CodeOf(err) == i18n.ErrInternal.Code {
			err = ErrSyncFailed.Wrap(err, "catalog", obj.Spec.Title, "reason", err.Error())
		}
		return nil, func(st *CatalogStatus) { st.SyncError, st.Official = reason, official }, err
	}
	c.mu.Lock()
	c.indexes[id] = idx
	c.mu.Unlock()
	now := c.opts.Now().UTC()
	sum := sha256.Sum256(data)
	built := idx.Built
	return idx, func(st *CatalogStatus) {
		st.SyncedAt, st.Built, st.Items, st.IndexSHA256, st.SyncError = &now, &built, len(idx.Items), hex.EncodeToString(sum[:]), ""
		st.Official = official
	}, nil
}

// keep writes a checked index and its signature to the cache.
func (c *Catalogs) keep(id store.ID, data, sig []byte) error {
	dir := c.dir(id)
	if dir == "" {
		return nil
	}
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, IndexName), data, 0o640); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, SignatureName), sig, 0o640)
}

func (c *Catalogs) fetchIndex(ctx context.Context, spec CatalogSpec) (*Index, []byte, []byte, error) {
	keys := keysOf(spec)
	var errs []error
	for _, base := range bases(spec) {
		data, err := c.get(ctx, base+"/"+IndexName, maxIndex)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		sig, err := c.get(ctx, base+"/"+SignatureName, ed25519.SignatureSize)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		idx, err := verify(data, sig, keys)
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", base, err))
			continue
		}
		return idx, data, sig, nil
	}
	return nil, nil, nil, errors.Join(errs...)
}

func (c *Catalogs) setStatus(ctx context.Context, id store.ID, change func(st *CatalogStatus)) {
	err := c.opts.Store.Write(ctx, store.WriteMeta{Actor: "system:catalogs"}, func(tx store.Tx) error {
		obj, err := CatalogKind.Get(ctx, tx, id)
		if err != nil {
			return err
		}
		st := obj.Status
		change(&st)
		raw, err := json.Marshal(st)
		if err != nil {
			return err
		}
		return tx.UpdateStatus(ctx, id, raw)
	})
	if err != nil && !store.ErrNotFound.Is(err) {
		c.opts.Logger.Warn("recording a catalog's status", "catalog", id, "error", err)
	}
}

// Index returns a catalog's index as last fetched, checking a kept copy
// again when the panel restarted since.
func (c *Catalogs) Index(ctx context.Context, id store.ID) (*Index, error) {
	c.mu.RLock()
	idx, ok := c.indexes[id]
	c.mu.RUnlock()
	if ok {
		return idx, nil
	}
	obj, err := CatalogKind.Get(ctx, c.opts.Store, id)
	if err != nil {
		return nil, err
	}
	dir := c.dir(id)
	if dir == "" {
		return nil, ErrNotSynced.New("catalog", obj.Spec.Title)
	}
	data, err := os.ReadFile(filepath.Join(dir, IndexName))
	if err != nil {
		return nil, ErrNotSynced.New("catalog", obj.Spec.Title)
	}
	sig, err := os.ReadFile(filepath.Join(dir, SignatureName))
	if err != nil {
		return nil, ErrNotSynced.New("catalog", obj.Spec.Title)
	}
	// The keys may have changed since; a kept index must still check out.
	if idx, err = verify(data, sig, keysOf(obj.Spec)); err != nil {
		return nil, ErrNotSynced.New("catalog", obj.Spec.Title)
	}
	c.mu.Lock()
	c.indexes[id] = idx
	c.mu.Unlock()
	return idx, nil
}

// Forget drops what the panel kept of a catalog, as when it is deleted or
// its keys change.
func (c *Catalogs) Forget(id store.ID) {
	c.mu.Lock()
	delete(c.indexes, id)
	c.mu.Unlock()
	if dir := c.dir(id); dir != "" {
		os.RemoveAll(dir)
	}
}

// ListedItem is an item of a catalog, as the marketplace lists it.
type ListedItem struct {
	IndexItem
	Catalog      store.ID `json:"catalog"`
	CatalogName  string   `json:"catalogName"`
	CatalogTitle string   `json:"catalogTitle"`
	// Official items are from Racksail's own catalog; the rest are
	// third-party and labelled so.
	Official bool `json:"official"`
}

// ItemFilter selects items.
type ItemFilter struct {
	Kind     ItemKind
	Category string
	// Query matches names, ids and summaries, ignoring case.
	Query string
	// Hidden includes broken and unmaintained items.
	Hidden bool
}

// Items lists the items of every catalog that is on, featured first and
// then by name.
func (c *Catalogs) Items(ctx context.Context, f ItemFilter) ([]ListedItem, error) {
	list, _, err := CatalogKind.List(ctx, c.opts.Store, store.Query{Limit: store.MaxLimit})
	if err != nil {
		return nil, err
	}
	q := strings.ToLower(strings.TrimSpace(f.Query))
	var out []ListedItem
	for _, cat := range list {
		if cat.Spec.Disabled || cat.Deleted() {
			continue
		}
		idx, err := c.Index(ctx, cat.ID)
		if err != nil {
			continue
		}
		official := c.official(cat.Spec)
		for _, it := range idx.Items {
			switch {
			case f.Kind != "" && it.Kind != f.Kind:
				continue
			case f.Category != "" && !slices.Contains(it.Categories, f.Category):
				continue
			case !f.Hidden && (it.Broken || it.Unmaintained):
				continue
			case q != "" && !strings.Contains(strings.ToLower(it.ID+" "+it.Name+" "+it.Summary), q):
				continue
			}
			out = append(out, ListedItem{IndexItem: it, Catalog: cat.ID, CatalogName: cat.Spec.Name, CatalogTitle: cat.Spec.Title, Official: official})
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Featured != out[j].Featured {
			return out[i].Featured
		}
		if out[i].Official != out[j].Official {
			return out[i].Official
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// Item returns one item of a catalog.
func (c *Catalogs) Item(ctx context.Context, catalog store.ID, item string) (*ListedItem, error) {
	obj, err := CatalogKind.Get(ctx, c.opts.Store, catalog)
	if err != nil {
		return nil, err
	}
	idx, err := c.Index(ctx, catalog)
	if err != nil {
		return nil, err
	}
	for _, it := range idx.Items {
		if it.ID == item {
			return &ListedItem{IndexItem: it, Catalog: obj.ID, CatalogName: obj.Spec.Name, CatalogTitle: obj.Spec.Title, Official: c.official(obj.Spec)}, nil
		}
	}
	return nil, ErrNoItem.New("catalog", obj.Spec.Title, "item", item)
}

// File fetches one file of an item, checked against the index, from the
// cache when it was fetched before.
func (c *Catalogs) File(ctx context.Context, catalog store.ID, item, file string) ([]byte, error) {
	it, err := c.Item(ctx, catalog, item)
	if err != nil {
		return nil, err
	}
	want := IndexFile{}
	for _, f := range it.Files {
		if f.Path == file {
			want = f
		}
	}
	if want.Path == "" && file == it.Path {
		want = IndexFile{Path: it.Path, SHA256: it.SHA256, Size: -1}
	}
	if want.Path == "" {
		return nil, ErrNoItem.New("catalog", it.CatalogTitle, "item", item+"/"+file)
	}
	cached := ""
	if dir := c.dir(catalog); dir != "" {
		cached = filepath.Join(dir, "files", want.SHA256)
		if data, err := os.ReadFile(cached); err == nil && hashOf(data) == want.SHA256 {
			return data, nil
		}
	}
	obj, err := CatalogKind.Get(ctx, c.opts.Store, catalog)
	if err != nil {
		return nil, err
	}
	var errs []error
	for _, base := range bases(obj.Spec) {
		data, err := c.get(ctx, base+"/"+path.Clean(want.Path), maxItemFile)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if hashOf(data) != want.SHA256 || (want.Size >= 0 && int64(len(data)) != want.Size) {
			errs = append(errs, ErrFileMismatch.New("file", want.Path, "catalog", obj.Spec.Title))
			continue
		}
		if cached != "" {
			if err := os.MkdirAll(filepath.Dir(cached), 0o750); err == nil {
				os.WriteFile(cached, data, 0o640)
			}
		}
		return data, nil
	}
	if len(errs) == 1 {
		return nil, errs[0]
	}
	return nil, ErrSyncFailed.Wrap(errors.Join(errs...), "catalog", obj.Spec.Title, "reason", errors.Join(errs...).Error())
}

func hashOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// Recipe fetches an item's recipe, checked against the index, and reads
// it.
func (c *Catalogs) Recipe(ctx context.Context, catalog store.ID, item string) (*Recipe, []byte, *ListedItem, error) {
	it, err := c.Item(ctx, catalog, item)
	if err != nil {
		return nil, nil, nil, err
	}
	data, err := c.File(ctx, catalog, item, it.Path)
	if err != nil {
		return nil, nil, nil, err
	}
	r, err := Parse(data)
	if err != nil {
		return nil, nil, nil, err
	}
	if r.ID != it.ID || r.Version != it.Version {
		return nil, nil, nil, ErrFileMismatch.New("file", it.Path, "catalog", it.CatalogTitle)
	}
	return r, data, it, nil
}

// Boot adds the official catalog the first time the panel starts, when the
// panel has keys for it.
func (c *Catalogs) Boot(ctx context.Context, submit func(ctx context.Context, req changeset.Request) (*changeset.ChangeSet, error)) error {
	if len(c.opts.Official) == 0 {
		return nil
	}
	list, _, err := CatalogKind.List(ctx, c.opts.Store, store.Query{Limit: store.MaxLimit})
	if err != nil {
		return err
	}
	for _, cat := range list {
		if cat.Spec.Name == "official" {
			return nil
		}
	}
	var keys []string
	for _, k := range c.opts.Official {
		keys = append(keys, base64.StdEncoding.EncodeToString(k))
	}
	spec, err := json.Marshal(CatalogSpec{Name: "official", Title: "Racksail catalog", URL: OfficialURL, Keys: keys})
	if err != nil {
		return err
	}
	_, err = submit(ctx, changeset.Request{
		Actor: auth.System("catalogs"), Title: "Add the official catalog",
		Changes: []changeset.Change{{Op: changeset.OpCreate, Kind: CatalogKind.Name(), Scope: store.ServerScope(), Spec: spec}},
	})
	return err
}

// syncEvery is how often catalogs are fetched while the marketplace is on.
const syncEvery = 24 * time.Hour

// Run fetches every catalog that is on once a day while the marketplace
// module is on, until ctx ends.
func (c *Catalogs) Run(ctx context.Context) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		if c.active() {
			c.syncDue(ctx)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}

func (c *Catalogs) syncDue(ctx context.Context) {
	list, _, err := CatalogKind.List(ctx, c.opts.Store, store.Query{Limit: store.MaxLimit})
	if err != nil {
		c.opts.Logger.Warn("listing catalogs", "error", err)
		return
	}
	now := c.opts.Now()
	for _, cat := range list {
		if cat.Spec.Disabled || cat.Deleted() {
			continue
		}
		if s := cat.Status.SyncedAt; s != nil && now.Sub(*s) < syncEvery && cat.Status.SyncError == "" {
			continue
		}
		if _, err := c.Sync(ctx, cat.ID); err != nil && ctx.Err() == nil {
			c.opts.Logger.Warn("fetching a catalog", "catalog", cat.Spec.Name, "error", err)
		}
	}
}

// Reconciler returns the reconciler of core.catalog: a catalog lives in the
// panel, so applying one fetches its index, when the catalog is added or
// changed while the marketplace module is on, and its action fetches it
// again.
func (c *Catalogs) Reconciler() reconcile.Reconciler {
	return reconcile.Typed(CatalogKind, catalogReconciler{c: c})
}

type catalogReconciler struct {
	c *Catalogs
}

func (r catalogReconciler) Plan(ctx context.Context, obj *store.Object[CatalogSpec, CatalogStatus], req reconcile.Request) (reconcile.Plan, error) {
	return reconcile.Plan{}, nil
}

func (r catalogReconciler) Apply(ctx context.Context, obj *store.Object[CatalogSpec, CatalogStatus], plan reconcile.Plan, ex reconcile.Executor) (CatalogStatus, error) {
	st := obj.Status
	if obj.Deleted() {
		r.c.Forget(obj.ID)
		return st, nil
	}
	// New keys or a new address: what was kept may no longer check out.
	// The resync applies a catalog that didn't change too, which keeps
	// what it has.
	changed := st.ObservedGeneration != obj.Generation
	if changed {
		r.c.mu.Lock()
		delete(r.c.indexes, obj.ID)
		r.c.mu.Unlock()
	}
	st.Official = r.c.official(obj.Spec)
	if err := reconcile.ApplySteps(ctx, obj.Generation, plan, ex, &st.Status, r.c.opts.Now()); err != nil {
		return st, err
	}
	if !changed || obj.Spec.Disabled || !r.c.active() {
		return st, nil
	}
	// A catalog that can't be fetched now is still kept, and says why; the
	// next daily fetch or Fetch now tries again.
	_, record, err := r.c.fetch(ctx, obj)
	record(&st)
	if err != nil {
		st.Phase, st.Message = store.PhaseDegraded, "Its index couldn't be fetched: "+reasonOf(err)
	}
	return st, nil
}

func (catalogReconciler) PlanAction(ctx context.Context, req reconcile.ActionRequest) (reconcile.ActionPlan, error) {
	return reconcile.ActionPlan{}, nil
}

func (r catalogReconciler) ApplyAction(ctx context.Context, req reconcile.ActionRequest) (any, error) {
	if req.Action != "sync" {
		return nil, fmt.Errorf("recipes: catalogs have no action called %q", req.Action)
	}
	idx, err := r.c.Sync(ctx, req.Resource.ID)
	if err != nil {
		return nil, err
	}
	return SyncOutput{Items: len(idx.Items), Built: idx.Built}, nil
}

// reasonOf is an error's message for people.
func reasonOf(err error) string {
	e := i18n.From(err)
	if e.Code == i18n.ErrInternal.Code {
		return err.Error()
	}
	return e.Message
}
