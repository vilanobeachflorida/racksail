package store

import (
	"context"
	"encoding/base64"
	"encoding/json"
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
)

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
}

// Version is one recorded spec version of a resource.
type Version struct {
	Generation int64             `json:"generation"`
	Spec       json.RawMessage   `json:"spec"`
	Labels     map[string]string `json:"labels,omitempty"`
	ChangeSet  ID                `json:"changeSet,omitempty"`
	Actor      string            `json:"actor,omitempty"`
	Time       time.Time         `json:"time"`
	Deleted    bool              `json:"deleted,omitempty"`
}

// Query selects resources for List.
type Query struct {
	// Kind limits the list to one kind. It is required.
	Kind string
	// Scope limits the list to one owner when set.
	Scope *Scope
	// Server limits the list to resources placed on one cluster server when set.
	Server *ID
	// Labels must all match.
	Labels map[string]string
	// Index matches values of indexes the kind declares, by index name.
	Index map[string]string
	// IDs limits the list to these resources when it isn't empty, as for an
	// actor whose grants name single resources.
	IDs []ID
	// IncludeDeleted also returns resources marked as deleted.
	IncludeDeleted bool
	// Cursor continues a previous list; it comes from Page.Next.
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
