package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Streams move data too large for an operation's JSON input or output:
// Btrfs send streams between servers, and big files such as uploads and
// downloads. They are validated, idempotent where that applies, and audited
// like operations.

// maxUploadBytes bounds a file uploaded through a stream.
const maxUploadBytes = 256 << 30

// Streamer moves data too large for operations.
type Streamer interface {
	// SendSnapshot writes a Btrfs send stream of a read-only snapshot to w.
	SendSnapshot(ctx context.Context, in SnapshotStreamInput, w io.Writer) error
	// ReceiveSnapshot receives a send stream from r into a directory.
	ReceiveSnapshot(ctx context.Context, dir string, r io.Reader) error
	// Download writes a file's contents to w.
	Download(ctx context.Context, path string, w io.Writer) error
	// Upload replaces a file with what r holds.
	Upload(ctx context.Context, in UploadInput, r io.Reader) error
}

// SnapshotStreamInput names a snapshot to send.
type SnapshotStreamInput struct {
	Snapshot string `json:"snapshot" doc:"A read-only snapshot"`
	// Parent makes the stream incremental: only what changed since it.
	Parent string `json:"parent,omitempty" doc:"An earlier snapshot the receiver already has"`
}

// Validate implements Input.
func (in SnapshotStreamInput) Validate() error {
	if err := checkRead(in.Snapshot); err != nil {
		return err
	}
	if in.Parent != "" {
		return checkRead(in.Parent)
	}
	return nil
}

// UploadInput describes a file to write from a stream.
type UploadInput struct {
	Path  string       `json:"path"`
	Mode  Mode         `json:"mode"`
	Owner system.Owner `json:"owner,omitzero"`
}

// Validate implements Input.
func (in UploadInput) Validate() error {
	if err := checkWrite(in.Path); err != nil {
		return err
	}
	if err := checkFileMode(in.Mode, false); err != nil {
		return err
	}
	return checkOwner(in.Owner)
}

type receiveInput struct {
	Dir string `json:"dir"`
}

func (in receiveInput) Validate() error { return checkWrite(in.Dir) }

type downloadInput struct {
	Path string `json:"path"`
}

func (in downloadInput) Validate() error { return checkRead(in.Path) }

// runStream validates, runs and audits one stream.
func runStream(ctx context.Context, env *Env, name string, in Input, run func() (bool, error)) error {
	start := time.Now()
	op := "streams/" + name
	if err := in.Validate(); err != nil {
		err = ErrInvalidInput.Wrap(err, "op", op, "reason", err.Error())
		env.audit(op, in, nil, nil, err, start)
		return err
	}
	changed, err := run()
	if err != nil {
		err = failed(op, err)
	}
	env.audit(op, in, nil, Result{Changed: changed}, err, start)
	return err
}

// checkSnapshots refuses snapshot paths that pass through a link, before
// btrfs is given them.
func checkSnapshots(ctx context.Context, env *Env, in SnapshotStreamInput) error {
	for _, p := range []string{in.Snapshot, in.Parent} {
		if p == "" {
			continue
		}
		if err := checkNoSymlinks(ctx, env, p); err != nil {
			return err
		}
	}
	return nil
}

func streamSend(ctx context.Context, env *Env, in SnapshotStreamInput, w io.Writer) error {
	return runStream(ctx, env, "SendSnapshot", in, func() (bool, error) {
		if err := checkSnapshots(ctx, env, in); err != nil {
			return false, err
		}
		return false, env.System.Btrfs.Send(ctx, in.Snapshot, in.Parent, w)
	})
}

func streamReceive(ctx context.Context, env *Env, dir string, r io.Reader) error {
	return runStream(ctx, env, "ReceiveSnapshot", receiveInput{Dir: dir}, func() (bool, error) {
		if err := checkNoSymlinks(ctx, env, dir); err != nil {
			return false, err
		}
		return true, receive(ctx, env, dir, r)
	})
}

// receive runs btrfs receive into dir and, when it fails, removes the
// subvolumes it left half received.
func receive(ctx context.Context, env *Env, dir string, r io.Reader) error {
	before, err := subvolumesIn(ctx, env, dir)
	if err != nil {
		return err
	}
	err = env.System.Btrfs.Receive(ctx, dir, r, receiveSources(ctx, env, dir))
	if err != nil {
		if cerr := removeNewSubvolumes(ctx, env, dir, before); cerr != nil {
			err = errors.Join(err, cerr)
		}
	}
	return err
}

// receiveSources decides which subvolumes a stream received into dir may
// take data from. btrfs receive looks the parent of an incremental stream,
// and the sources of data shared between files, up by UUID anywhere on the
// filesystem and copies from them, so every subvolume carrying the UUID
// must be one Racksail reads in full, such as an earlier snapshot of the
// same data, never the system's own subvolumes or their snapshots.
func receiveSources(ctx context.Context, env *Env, dir string) func(uuid string) error {
	return func(uuid string) error {
		svs, err := env.System.Btrfs.Subvolumes(ctx, dir)
		if err != nil {
			return err
		}
		found := false
		for _, sv := range svs {
			if !strings.EqualFold(sv.UUID, uuid) && !strings.EqualFold(sv.ReceivedUUID, uuid) {
				continue
			}
			found = true
			if checkRead(sv.Path) != nil {
				return fmt.Errorf("subvolume %s is %s, which Racksail doesn't read: %w", uuid, sv.Path, system.ErrInvalid)
			}
		}
		if !found {
			return fmt.Errorf("no subvolume %s is on the filesystem of %s: %w", uuid, dir, system.ErrNotFound)
		}
		return nil
	}
}

// removeNewSubvolumes deletes the subvolumes below dir that weren't there
// before.
func removeNewSubvolumes(ctx context.Context, env *Env, dir string, before map[string]bool) error {
	after, err := subvolumesIn(ctx, env, dir)
	if err != nil {
		return err
	}
	var errs []error
	for p := range after {
		if !before[p] {
			if err := env.System.Btrfs.DeleteSubvolume(ctx, p); err != nil {
				errs = append(errs, err)
			}
		}
	}
	return errors.Join(errs...)
}

func streamDownload(ctx context.Context, env *Env, p string, w io.Writer) error {
	return runStream(ctx, env, "Download", downloadInput{Path: p}, func() (bool, error) {
		files, err := beneath(env, p, true)
		if err != nil {
			return false, err
		}
		f, err := files.Open(ctx, p)
		if err != nil {
			return false, err
		}
		defer f.Close()
		_, err = io.Copy(w, f)
		return false, err
	})
}

func streamUpload(ctx context.Context, env *Env, in UploadInput, r io.Reader) error {
	return runStream(ctx, env, "Upload", in, func() (bool, error) {
		files, err := beneath(env, in.Path, false)
		if err != nil {
			return false, err
		}
		limited := &limitedReader{r: r, left: maxUploadBytes}
		if err := files.WriteFrom(ctx, in.Path, limited, in.Mode.FileMode(), in.Owner); err != nil {
			return false, err
		}
		if limited.over {
			files.Remove(ctx, in.Path)
			return false, fmt.Errorf("the upload is larger than %d bytes: %w", int64(maxUploadBytes), system.ErrTooLarge)
		}
		env.forget(ctx, in.Path)
		return true, nil
	})
}

// limitedReader stops after left bytes and notes that there was more.
type limitedReader struct {
	r    io.Reader
	left int64
	over bool
}

func (l *limitedReader) Read(p []byte) (int, error) {
	if l.left <= 0 {
		var one [1]byte
		if n, _ := l.r.Read(one[:]); n > 0 {
			l.over = true
		}
		return 0, io.EOF
	}
	if int64(len(p)) > l.left {
		p = p[:l.left]
	}
	n, err := l.r.Read(p)
	l.left -= int64(n)
	return n, err
}

// SendSnapshot implements Streamer.
func (l *Local) SendSnapshot(ctx context.Context, in SnapshotStreamInput, w io.Writer) error {
	return streamSend(ctx, l.env(), in, w)
}

// ReceiveSnapshot implements Streamer.
func (l *Local) ReceiveSnapshot(ctx context.Context, dir string, r io.Reader) error {
	return streamReceive(ctx, l.env(), dir, r)
}

// Download implements Streamer.
func (l *Local) Download(ctx context.Context, p string, w io.Writer) error {
	return streamDownload(ctx, l.env(), p, w)
}

// Upload implements Streamer.
func (l *Local) Upload(ctx context.Context, in UploadInput, r io.Reader) error {
	return streamUpload(ctx, l.env(), in, r)
}

func (l *Local) env() *Env {
	return &Env{System: l.backend, Log: l.log, Runtime: l.runtime, Caller: CallerLocal}
}

// registerStreams adds the stream endpoints, documented with the
// operations.
func (s *Server) registerStreams() {
	doc := s.api.OpenAPI()
	schema := func(t reflect.Type) *huma.Schema { return doc.Components.Schemas.Schema(t, true, "") }
	binary := &huma.Schema{Type: "string", Format: "binary"}
	problem := map[string]*huma.MediaType{"application/problem+json": {Schema: schema(reflect.TypeFor[problemDoc]())}}
	for _, st := range []struct {
		name, summary, description string
		in                         reflect.Type
		query                      []*huma.Param
		upload                     bool
	}{
		{"SendSnapshot", "Stream a Btrfs snapshot", "Answers with the Btrfs send stream of a read-only snapshot, incremental from parent when it's given.", reflect.TypeFor[SnapshotStreamInput](), nil, false},
		{"ReceiveSnapshot", "Receive a Btrfs snapshot", "Receives the send stream in the request body into a directory on Btrfs.", nil,
			[]*huma.Param{{Name: "dir", In: "query", Required: true, Description: "Directory the snapshot is received into", Schema: &huma.Schema{Type: "string"}}}, true},
		{"Download", "Download a file", "Answers with the contents of a file inside a location Racksail reads.", reflect.TypeFor[downloadInput](), nil, false},
		{"Upload", "Upload a file", "Replaces a file inside a location Racksail manages with the request body, atomically.", nil, []*huma.Param{
			{Name: "path", In: "query", Required: true, Description: "Absolute path of the file", Schema: &huma.Schema{Type: "string"}},
			{Name: "mode", In: "query", Required: true, Description: "Permission bits in octal, such as 0644", Schema: &huma.Schema{Type: "string"}},
			{Name: "user", In: "query", Description: "Owner; root when empty", Schema: &huma.Schema{Type: "string"}},
			{Name: "group", In: "query", Description: "Group; the owner's when empty", Schema: &huma.Schema{Type: "string"}},
		}, true},
	} {
		op := &huma.Operation{
			OperationID: "stream" + st.name, Method: http.MethodPost, Path: "/v1/streams/" + st.name,
			Summary: st.summary, Description: st.description, Tags: []string{"streams"}, Parameters: st.query,
			Responses: map[string]*huma.Response{"default": {Description: "Error", Content: problem}},
		}
		if st.upload {
			op.RequestBody = &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{"application/octet-stream": {Schema: binary}}}
			op.Responses["204"] = &huma.Response{Description: "Done"}
		} else {
			op.RequestBody = &huma.RequestBody{Required: true, Content: map[string]*huma.MediaType{"application/json": {Schema: schema(st.in)}}}
			op.Responses["200"] = &huma.Response{Description: "The data", Content: map[string]*huma.MediaType{"application/octet-stream": {Schema: binary}}}
		}
		doc.AddOperation(op)
	}

	s.mux.HandleFunc("POST /v1/streams/SendSnapshot", func(w http.ResponseWriter, r *http.Request) {
		var in SnapshotStreamInput
		if !decodeStreamInput(w, r, "SendSnapshot", &in) {
			return
		}
		s.download(w, r, func(out io.Writer) error { return streamSend(r.Context(), s.env(r.Context(), false, ""), in, out) })
	})
	s.mux.HandleFunc("POST /v1/streams/Download", func(w http.ResponseWriter, r *http.Request) {
		var in downloadInput
		if !decodeStreamInput(w, r, "Download", &in) {
			return
		}
		if in.Validate() == nil {
			if files, err := beneathRoot(s.backend.Files, in.Path, true); err == nil {
				if info, err := files.Stat(r.Context(), in.Path); err == nil && info.Mode.IsRegular() {
					w.Header().Set("Content-Length", strconv.FormatInt(info.Size, 10))
				}
			}
		}
		s.download(w, r, func(out io.Writer) error {
			return streamDownload(r.Context(), s.env(r.Context(), false, ""), in.Path, out)
		})
	})
	s.mux.HandleFunc("POST /v1/streams/ReceiveSnapshot", func(w http.ResponseWriter, r *http.Request) {
		err := streamReceive(r.Context(), s.env(r.Context(), false, ""), r.URL.Query().Get("dir"), r.Body)
		s.done(w, err)
	})
	s.mux.HandleFunc("POST /v1/streams/Upload", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		in := UploadInput{Path: q.Get("path"), Owner: system.Owner{User: q.Get("user"), Group: q.Get("group")}}
		if err := in.Mode.UnmarshalText([]byte(q.Get("mode"))); err != nil {
			writeProblem(w, ErrInvalidInput.Wrap(err, "op", "streams/Upload", "reason", err.Error()))
			return
		}
		s.done(w, streamUpload(r.Context(), s.env(r.Context(), false, ""), in, r.Body))
	})
}

// problemDoc documents error answers of the streams.
type problemDoc struct {
	Code   string `json:"code"`
	Status int    `json:"status"`
	Detail string `json:"detail"`
}

func decodeStreamInput(w http.ResponseWriter, r *http.Request, name string, v any) bool {
	dec := json.NewDecoder(io.LimitReader(r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeProblem(w, ErrInvalidInput.Wrap(err, "op", "streams/"+name, "reason", err.Error()))
		return false
	}
	return true
}

// download answers with a stream. Errors before the first byte become
// problem details; once data flows, a failure can only cut the stream
// short, which the client sees as an error reading it.
func (s *Server) download(w http.ResponseWriter, r *http.Request, run func(io.Writer) error) {
	fw := &firstWrite{w: w}
	err := run(fw)
	if err != nil && !fw.started {
		w.Header().Del("Content-Length")
		writeProblem(w, err)
		return
	}
	if err != nil {
		s.log.Warn("stream cut short", "path", r.URL.Path, "error", err)
		panic(http.ErrAbortHandler)
	}
	if !fw.started {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
	}
}

func (s *Server) done(w http.ResponseWriter, err error) {
	if err != nil {
		writeProblem(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// firstWrite sets the content type when the first byte goes out.
type firstWrite struct {
	w       http.ResponseWriter
	started bool
}

func (f *firstWrite) Write(p []byte) (int, error) {
	if !f.started {
		f.started = true
		f.w.Header().Set("Content-Type", "application/octet-stream")
		f.w.WriteHeader(http.StatusOK)
	}
	return f.w.Write(p)
}

// SendSnapshot implements Streamer.
func (c *HTTPClient) SendSnapshot(ctx context.Context, in SnapshotStreamInput, w io.Writer) error {
	body, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return c.stream(ctx, "SendSnapshot", nil, "application/json", bytes.NewReader(body), w)
}

// ReceiveSnapshot implements Streamer.
func (c *HTTPClient) ReceiveSnapshot(ctx context.Context, dir string, r io.Reader) error {
	return c.stream(ctx, "ReceiveSnapshot", url.Values{"dir": {dir}}, "application/octet-stream", r, nil)
}

// Download implements Streamer.
func (c *HTTPClient) Download(ctx context.Context, p string, w io.Writer) error {
	body, err := json.Marshal(downloadInput{Path: p})
	if err != nil {
		return err
	}
	return c.stream(ctx, "Download", nil, "application/json", bytes.NewReader(body), w)
}

// Upload implements Streamer.
func (c *HTTPClient) Upload(ctx context.Context, in UploadInput, r io.Reader) error {
	mode, _ := in.Mode.MarshalText()
	q := url.Values{"path": {in.Path}, "mode": {string(mode)}}
	if in.Owner.User != "" {
		q.Set("user", in.Owner.User)
	}
	if in.Owner.Group != "" {
		q.Set("group", in.Owner.Group)
	}
	return c.stream(ctx, "Upload", q, "application/octet-stream", r, nil)
}

func (c *HTTPClient) stream(ctx context.Context, name string, q url.Values, contentType string, body io.Reader, w io.Writer) error {
	target := c.base + "/v1/streams/" + name
	if len(q) > 0 {
		target += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := c.http.Do(req)
	if err != nil {
		return ErrUnreachable.Wrap(err, "reason", err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		return problemError(req.URL.Path, resp.Status, data)
	}
	if w == nil {
		return nil
	}
	if _, err := io.Copy(w, resp.Body); err != nil {
		return ErrUnreachable.Wrap(err, "reason", err.Error())
	}
	return nil
}
