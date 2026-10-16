package agent

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sync"
	"syscall"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/store"
)

// AuditRecord is one operation the agent ran or refused. It never holds
// what an operation was given, only a hash of it and the names it touched.
type AuditRecord struct {
	Time time.Time `json:"time"`
	Op   string    `json:"op"`
	// Caller is who asked: "local" on the Unix socket and in-process, or
	// the identity in the certificate of a remote panel.
	Caller string   `json:"caller,omitempty" doc:"Who asked: local, or the remote panel's identity"`
	Owner  store.ID `json:"owner,omitempty" doc:"The resource the call was made for"`
	Check  bool     `json:"check,omitempty" doc:"Only reported what would change"`
	// Targets are the paths, units and names the operation touched.
	Targets     []string      `json:"targets,omitempty"`
	InputSHA256 string        `json:"inputSha256"`
	Changed     bool          `json:"changed"`
	Duration    time.Duration `json:"duration"`
	Code        string        `json:"code,omitempty" doc:"Error code when the operation failed"`
	Error       string        `json:"error,omitempty"`
	// Remote is where a caller the agent refused connected from.
	Remote string `json:"remote,omitempty" doc:"Where a refused caller connected from"`
}

// Failed reports whether the operation failed.
func (r AuditRecord) Failed() bool { return r.Code != "" || r.Error != "" }

// AuditFilter narrows what Recent returns. Empty fields match everything.
type AuditFilter struct {
	Op     string    `json:"op,omitempty"`
	Owner  store.ID  `json:"owner,omitempty"`
	Since  time.Time `json:"since,omitzero"`
	Failed bool      `json:"failed,omitempty" doc:"Only failed operations"`
	Limit  int       `json:"limit,omitempty"`
}

func (f AuditFilter) matches(r AuditRecord) bool {
	return (f.Op == "" || f.Op == r.Op) && (f.Owner == "" || f.Owner == r.Owner) &&
		(f.Since.IsZero() || !r.Time.Before(f.Since)) && (!f.Failed || r.Failed())
}

// Auditor keeps the agent's audit trail.
type Auditor interface {
	Record(r AuditRecord) error
	// Recent returns matching records, newest first.
	Recent(f AuditFilter) ([]AuditRecord, error)
}

// MemoryAudit keeps the latest records in memory.
type MemoryAudit struct {
	mu      sync.Mutex
	records []AuditRecord
	limit   int
}

// NewMemoryAudit keeps up to limit records.
func NewMemoryAudit(limit int) *MemoryAudit {
	if limit <= 0 {
		limit = 10000
	}
	return &MemoryAudit{limit: limit}
}

// Record implements Auditor.
func (m *MemoryAudit) Record(r AuditRecord) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records = append(m.records, r)
	if len(m.records) > m.limit+m.limit/10 {
		m.records = slices.Clone(m.records[len(m.records)-m.limit:])
	}
	return nil
}

// Recent implements Auditor.
func (m *MemoryAudit) Recent(f AuditFilter) ([]AuditRecord, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return newestMatching(m.records, f), nil
}

func newestMatching(records []AuditRecord, f AuditFilter) []AuditRecord {
	limit := f.Limit
	if limit <= 0 {
		limit = 200
	}
	out := []AuditRecord{}
	for i := len(records) - 1; i >= 0 && len(out) < limit; i-- {
		if f.matches(records[i]) {
			out = append(out, records[i])
		}
	}
	return out
}

// FileAudit appends records to a file as JSON lines, starting a new file
// when it reaches its size limit and keeping one previous file. Recent reads
// both, newest first. The file is never reached through a symbolic link,
// and a file that was moved or removed under it is started again, so
// records never go to a file nobody can find.
type FileAudit struct {
	mu      sync.Mutex
	path    string
	maxSize int64
	file    *os.File
	size    int64
}

// OpenFileAudit opens path for appending, creating it and its directory
// readable by root only.
func OpenFileAudit(path string, maxSize int64) (*FileAudit, error) {
	if maxSize <= 0 {
		maxSize = 64 << 20
	}
	a := &FileAudit{path: path, maxSize: maxSize}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := a.open(); err != nil {
		return nil, err
	}
	return a, nil
}

// auditFlags open the trail for appending without following a link to
// somewhere else.
const auditFlags = os.O_WRONLY | os.O_APPEND | os.O_CREATE | syscall.O_NOFOLLOW

func (a *FileAudit) open() error {
	f, err := os.OpenFile(a.path, auditFlags, 0o600)
	if err != nil {
		return fmt.Errorf("opening the audit trail: %w", err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	if !fi.Mode().IsRegular() {
		f.Close()
		return fmt.Errorf("the audit trail %s isn't a regular file", a.path)
	}
	a.file, a.size = f, fi.Size()
	return nil
}

// current reports whether the open file is still the one at the path.
func (a *FileAudit) current() bool {
	open, err := a.file.Stat()
	if err != nil {
		return false
	}
	named, err := os.Lstat(a.path)
	return err == nil && os.SameFile(open, named)
}

// Record implements Auditor.
func (a *FileAudit) Record(r AuditRecord) error {
	line, err := json.Marshal(r)
	if err != nil {
		return err
	}
	line = append(line, '\n')
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.current() {
		a.file.Close()
		if err := a.open(); err != nil {
			return err
		}
	}
	if a.size+int64(len(line)) > a.maxSize {
		if err := a.rotate(); err != nil {
			return err
		}
	}
	n, err := a.file.Write(line)
	a.size += int64(n)
	return err
}

func (a *FileAudit) rotate() error {
	if err := a.file.Close(); err != nil {
		return err
	}
	if err := os.Rename(a.path, a.path+".1"); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return a.open()
}

// Recent implements Auditor.
func (a *FileAudit) Recent(f AuditFilter) ([]AuditRecord, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	var all []AuditRecord
	for _, p := range []string{a.path + ".1", a.path} {
		records, err := readAuditFile(p)
		if err != nil {
			return nil, err
		}
		all = append(all, records...)
	}
	return newestMatching(all, f), nil
}

func readAuditFile(p string) ([]AuditRecord, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []AuditRecord
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		var r AuditRecord
		if json.Unmarshal(sc.Bytes(), &r) == nil {
			out = append(out, r)
		}
	}
	return out, sc.Err()
}

// Close closes the file.
func (a *FileAudit) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.file.Close()
}

// targetFields are the input fields that name what an operation touches.
var targetFields = []string{"Path", "Paths", "Dest", "Source", "Name", "Names", "Unit", "Units", "Snapshot", "Dir", "Archive", "Root", "Device", "Disk", "Target", "Set", "Patterns", "Key", "Keys", "Packages"}

// auditTargets lists the names an input touches, from its well-known
// fields, never from contents.
func auditTargets(in any) []string {
	var out []string
	add := func(s string) {
		if s != "" && len(out) < 16 && len(s) <= 256 {
			out = append(out, s)
		}
	}
	v := reflect.Indirect(reflect.ValueOf(in))
	if v.Kind() != reflect.Struct {
		return nil
	}
	for _, name := range targetFields {
		f := v.FieldByName(name)
		if !f.IsValid() {
			continue
		}
		switch f.Kind() {
		case reflect.String:
			add(f.String())
		case reflect.Slice:
			for i := 0; i < f.Len(); i++ {
				switch e := f.Index(i); e.Kind() {
				case reflect.String:
					add(e.String())
				case reflect.Struct:
					if n := e.FieldByName("Name"); n.IsValid() && n.Kind() == reflect.String {
						add(n.String())
					}
				}
			}
		case reflect.Struct:
			for _, sub := range []string{"Dest", "Set", "Name"} {
				if s := f.FieldByName(sub); s.IsValid() && s.Kind() == reflect.String {
					add(s.String())
				}
			}
		}
	}
	return out
}

func inputHash(input []byte) string {
	sum := sha256.Sum256(input)
	return hex.EncodeToString(sum[:])
}

// auditError splits an error into its code and message for the record.
func auditError(err error) (code, message string) {
	if err == nil {
		return "", ""
	}
	if c := errorCode(err); c != "" {
		code = c
	}
	message = err.Error()
	if len(message) > 1024 {
		message = message[:1024] + "..."
	}
	return code, message
}

// String renders a record for the logs.
func (r AuditRecord) String() string {
	outcome := "unchanged"
	switch {
	case r.Failed():
		outcome = "failed"
	case r.Changed:
		outcome = "changed"
	}
	return fmt.Sprintf("%s %s by %s: %s", r.Op, r.Targets, r.Caller, outcome)
}
