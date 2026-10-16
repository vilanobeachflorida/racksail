package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
)

// Errors returned by every Store implementation. They carry i18n codes, so
// the API maps them to problem details without extra work.
var (
	// ErrNotFound means no resource has the requested ID.
	ErrNotFound = i18n.ErrNotFound
	// ErrConflict means the resource changed since the caller read it.
	ErrConflict = i18n.ErrConflict
	// ErrExists means the ID or a unique index value is already taken.
	ErrExists = i18n.ErrAlreadyExists
	// ErrIDUsed refuses a new resource with the ID of one that was purged;
	// see CheckReuse.
	ErrIDUsed = i18n.Define("store.id_used", http.StatusConflict, "{id} belonged to a resource that was deleted, so a new resource can't use it. Leave the ID out to get a new one.")
)

// CheckReuse decides whether r may be created although its ID has history,
// oldest version first. A purged resource keeps its history and the secrets
// it stored, for undo, so its ID only comes back for the undo of the change
// set that deleted it, as the same kind with the same owner; anything else
// would hand them to a new resource, perhaps of another account. undoOf is
// the change set the write undoes, if any.
func CheckReuse(history []Version, r *Resource, undoOf ID) error {
	if len(history) == 0 {
		return nil
	}
	last := history[len(history)-1]
	sameOwner := last.Scope.Level == "" || last.Scope == r.Scope
	if undoOf != "" && last.Deleted && last.ChangeSet == undoOf && last.Kind == r.Kind && sameOwner {
		return nil
	}
	return ErrIDUsed.New("id", string(r.ID))
}

// Reader reads resources.
type Reader interface {
	// Get returns the resource with the given ID, including deleted ones that
	// haven't been purged yet.
	Get(ctx context.Context, id ID) (*Resource, error)
	// List returns one page of resources matching q, ordered by ID, which is
	// creation order.
	List(ctx context.Context, q Query) (*Page, error)
}

// Tx is a write transaction. Everything written through a Tx is committed
// together or not at all.
type Tx interface {
	Reader
	// Create stores a new resource. An empty ID is filled with a new ID using
	// the kind's prefix. Generation and Revision start at 1.
	Create(ctx context.Context, r *Resource) error
	// Update replaces the spec, labels and placement of an existing resource.
	// r.Revision must match the stored revision, or be zero to skip the check.
	// The generation increases when the spec or placement changes. The kind,
	// scope and creation time can't change. On success r holds the stored
	// metadata.
	Update(ctx context.Context, r *Resource) error
	// UpdateStatus replaces the status. It changes neither the generation nor
	// the revision, so reconcilers never cause edit conflicts.
	UpdateStatus(ctx context.Context, id ID, status json.RawMessage) error
	// Delete marks a resource as deleted. Its reconciler removes what it
	// created on the machine and then purges it. revision works as in Update.
	Delete(ctx context.Context, id ID, revision int64) error
	// Purge removes a resource for good, keeping its history.
	Purge(ctx context.Context, id ID) error
}

// Store is where every resource lives.
type Store interface {
	Reader
	// Write runs fn in a transaction. If fn returns an error nothing is
	// written. meta is recorded with every spec version fn writes.
	Write(ctx context.Context, meta WriteMeta, fn func(tx Tx) error) error
	// History returns every recorded spec version of a resource, oldest first.
	History(ctx context.Context, id ID) ([]Version, error)
	// Watch returns the writes committed from now on that f selects, as
	// they are committed. Slow watchers miss changes rather than slowing
	// writers down; Watcher.Dropped says how many.
	Watch(f WatchFilter, buffer int) *Watcher
	// Close releases the store's resources.
	Close() error
}

// WriteMeta says why a write happened. It is stored with each spec version so
// history, undo and the audit log can explain every change.
type WriteMeta struct {
	// ChangeSet is the change set that made the write.
	ChangeSet ID
	// Actor describes who acted, for example "user:user_01..." or
	// "automation:auto_01... for user:user_01...".
	Actor string
	// UndoOf is the change set the write undoes. Only then may a resource
	// that change set deleted, and that was purged since, be created again
	// under its old ID.
	UndoOf ID
}

// Version is one recorded spec version of a resource.
type Version struct {
	Generation int64 `json:"generation" doc:"The generation this version of the spec had"`
	// Kind and Scope are the resource's, which never change, kept so a
	// resource brought back under its ID can be checked against the one it
	// was.
	Kind   string            `json:"-"`
	Scope  Scope             `json:"scope,omitzero" doc:"Who the resource belonged to"`
	Spec   json.RawMessage   `json:"spec" doc:"The desired state as it was; secret fields hold references, never values"`
	Labels map[string]string `json:"labels,omitempty"`
	// Server is the cluster server the resource was placed on.
	Server    ID        `json:"server,omitempty" doc:"The cluster server the resource was placed on; empty for the panel's own server"`
	ChangeSet ID        `json:"changeSet,omitempty" doc:"The change set that wrote this version"`
	Actor     string    `json:"actor,omitempty" doc:"Who made the change, and what acted for them"`
	Time      time.Time `json:"time"`
	Deleted   bool      `json:"deleted,omitempty" doc:"Set on the version recorded when the resource was deleted"`
}

// Query selects resources for List.
type Query struct {
	// Kind limits the list to one kind. It is required.
	Kind string
	// Scope limits the list to one owner when set.
	Scope *Scope
	// Scopes limits the list to resources of any of these owners when it
	// isn't empty, as for an actor with grants on several accounts. Scope,
	// when set, narrows it further.
	Scopes []Scope
	// Server limits the list to resources placed on one cluster server when set.
	Server *ID
	// Labels must all match.
	Labels map[string]string
	// Index matches values of indexes the kind declares, by index name.
	Index map[string]string
	// Phases limits the list to resources in any of these phases. A
	// resource no reconciler has looked at yet is pending.
	Phases []Phase
	// Search keeps resources whose ID, indexed values, title or subtitle
	// contain the text, ignoring the case of ASCII letters.
	Search string
	// IDs limits the list to these resources when it isn't empty, as for an
	// actor whose grants name single resources.
	IDs []ID
	// IncludeDeleted also returns resources marked as deleted.
	IncludeDeleted bool
	// Unreconciled keeps resources whose latest generation no reconciler
	// has carried out yet, and resources being removed. It implies
	// IncludeDeleted for those.
	Unreconciled bool
	// Sort orders the list by id, createdAt, updatedAt, generation, phase or
	// the name of an index, with "-" in front for descending order. Empty
	// means id, which is creation order. Resources with the same value are
	// ordered by ID.
	Sort string
	// Cursor continues a previous list; it comes from Page.Next and only
	// works with the same Sort.
	Cursor string
	// Limit is the page size. Zero means DefaultLimit; it is capped at MaxLimit.
	Limit int
}

// Page sizes.
const (
	DefaultLimit = 50
	MaxLimit     = 1000
)

// Page is one page of a list.
type Page struct {
	Items []*Resource
	// Next is the cursor for the following page, empty on the last page.
	Next string
}

// EffectiveLimit returns the page size a query asks for, within bounds.
func (q Query) EffectiveLimit() int {
	switch {
	case q.Limit <= 0:
		return DefaultLimit
	case q.Limit > MaxLimit:
		return MaxLimit
	}
	return q.Limit
}

// EncodeCursor turns the last ID of a page into an opaque cursor.
func EncodeCursor(last ID) string {
	return base64.RawURLEncoding.EncodeToString([]byte(last))
}

// DecodeCursor reverses EncodeCursor.
func DecodeCursor(cursor string) (ID, error) {
	b, err := base64.RawURLEncoding.DecodeString(cursor)
	if err != nil {
		return "", i18n.ErrBadRequest.Wrap(err, "reason", "the cursor is malformed")
	}
	id, err := ParseID(string(b))
	if err != nil {
		return "", i18n.ErrBadRequest.Wrap(err, "reason", "the cursor is malformed")
	}
	return id, nil
}

// SetStatus writes the status of one resource in its own transaction. It is
// what reconcilers use after applying a plan.
func SetStatus(ctx context.Context, s Store, id ID, status any) error {
	raw, err := json.Marshal(status)
	if err != nil {
		return err
	}
	return s.Write(ctx, WriteMeta{}, func(tx Tx) error {
		return tx.UpdateStatus(ctx, id, raw)
	})
}
