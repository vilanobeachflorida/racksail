package agent

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"

	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/system"
	"github.com/vilanobeachflorida/racksail/internal/system/sim"
)

func TestArchives(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	b := m.Backend()
	root := "/srv/racksail/accounts/shop"
	if _, err := EnsureDir.Call(ctx, c, EnsureDirInput{Path: root + "/site/assets", Mode: 0o755, Parents: true}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"site/index.html": "home", "site/assets/app.css": "body{}", "site/.git": "skip me"} {
		if _, err := WriteFile.Call(ctx, c, WriteFileInput{Path: root + "/" + name, Content: []byte(content), Mode: 0o644}, CallOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	for _, format := range []system.ArchiveFormat{"tar.gz", "tar.zst", "zip", "tar"} {
		dest := "backup." + string(format)
		pack := CreateArchiveInput{Root: root, Source: "site", Dest: dest, Format: format, Exclude: []string{".git"}, Mode: 0o600}
		if res, err := CreateArchive.Call(ctx, c, pack, CallOptions{Check: true}); err != nil || !res.Changed {
			t.Fatalf("%s: check = %+v, %v", format, res, err)
		}
		res, err := CreateArchive.Call(ctx, c, pack, CallOptions{})
		if err != nil || !res.Changed || res.Stats.Files != 2 {
			t.Fatalf("%s: pack = %+v, %v", format, res, err)
		}
		into := "restored-" + strings.ReplaceAll(string(format), ".", "-")
		res, err = ExtractArchive.Call(ctx, c, ExtractArchiveInput{Root: root, Archive: dest, Dest: into, Format: format}, CallOptions{})
		if err != nil || res.Stats.Files != 2 {
			t.Fatalf("%s: unpack = %+v, %v", format, res, err)
		}
		got, err := b.Files.ReadFile(ctx, root+"/"+into+"/assets/app.css", 0)
		if err != nil || string(got) != "body{}" {
			t.Errorf("%s: restored file = %q, %v", format, got, err)
		}
		if _, err := b.Files.Stat(ctx, root+"/"+into+"/.git"); !errors.Is(err, system.ErrNotFound) {
			t.Errorf("%s: excluded files must stay out: %v", format, err)
		}
		// A destination that isn't empty is only unpacked into on request.
		if _, err := ExtractArchive.Call(ctx, c, ExtractArchiveInput{Root: root, Archive: dest, Dest: into, Format: format}, CallOptions{}); !errors.Is(err, ErrConflict) {
			t.Errorf("%s: unpacking over files: %v", format, err)
		}
		if _, err := ExtractArchive.Call(ctx, c, ExtractArchiveInput{Root: root, Archive: dest, Dest: into, Format: format, Merge: true}, CallOptions{}); err != nil {
			t.Errorf("%s: merging: %v", format, err)
		}
	}

	// An archive whose entries climb out of the destination is refused.
	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gz)
	evil := []byte("owned")
	tw.WriteHeader(&tar.Header{Name: "../../../../etc/racksail/evil", Mode: 0o644, Size: int64(len(evil)), Typeflag: tar.TypeReg})
	tw.Write(evil)
	tw.Close()
	gz.Close()
	if _, err := WriteFile.Call(ctx, c, WriteFileInput{Path: root + "/evil.tar.gz", Content: raw.Bytes(), Mode: 0o644}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := ExtractArchive.Call(ctx, c, ExtractArchiveInput{Root: root, Archive: "evil.tar.gz", Dest: "x", Format: "tar.gz"}, CallOptions{}); err == nil {
		t.Error("an archive escaping its destination must be refused")
	}
	if _, err := b.Files.Stat(ctx, "/etc/racksail/evil"); !errors.Is(err, system.ErrNotFound) {
		t.Errorf("the escaping entry was written: %v", err)
	}

	for _, bad := range []CreateArchiveInput{
		{Root: "/etc", Source: ".", Dest: "a.tar", Format: "tar", Mode: 0o600},
		{Root: root, Source: "../..", Dest: "a.tar", Format: "tar", Mode: 0o600},
		{Root: root, Source: "site", Dest: "/tmp/a.tar", Format: "tar", Mode: 0o600},
		{Root: root, Source: "site", Dest: "a.rar", Format: "rar", Mode: 0o600},
	} {
		if _, err := CreateArchive.Call(ctx, c, bad, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%+v: %v", bad, err)
		}
	}
}

func TestSnapshotFiles(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	b := m.Backend()
	blog := "/srv/racksail/accounts/blog"
	if _, err := EnsureSubvolume.Call(ctx, c, SubvolumeInput{Path: blog}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteFile.Call(ctx, c, WriteFileInput{Path: blog + "/index.html", Content: []byte("v1"), Mode: 0o644}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureDir.Call(ctx, c, EnsureDirInput{Path: SnapshotDir, Mode: 0o700}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	snap := SnapshotDir + "/blog-1"
	if _, err := CreateSnapshot.Call(ctx, c, SnapshotInput{Source: blog, Dest: snap, ReadOnly: true}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureDir.Call(ctx, c, EnsureDirInput{Path: "/srv/racksail/backups", Mode: 0o700}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	save := SaveSnapshotInput{Snapshot: snap, Dest: "/srv/racksail/backups/blog-1.btrfs"}
	res, err := SaveSnapshot.Call(ctx, c, save, CallOptions{})
	if err != nil || !res.Changed || res.Bytes == 0 {
		t.Fatalf("save = %+v, %v", res, err)
	}
	if again, _ := SaveSnapshot.Call(ctx, c, save, CallOptions{}); again.Changed || again.Bytes != res.Bytes {
		t.Errorf("saving again = %+v", again)
	}
	if info, _ := b.Files.Stat(ctx, save.Dest); info.Mode.Perm() != 0o600 {
		t.Errorf("mode = %v", info.Mode)
	}
	if _, err := EnsureSubvolume.Call(ctx, c, SubvolumeInput{Path: "/srv/racksail/restore"}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	restore := RestoreSnapshotInput{File: save.Dest, Dir: "/srv/racksail/restore"}
	if res, err := RestoreSnapshot.Call(ctx, c, restore, CallOptions{}); err != nil || !res.Changed {
		t.Fatalf("restore = %+v, %v", res, err)
	}
	got, err := b.Files.ReadFile(ctx, "/srv/racksail/restore/blog-1/index.html", 0)
	if err != nil || string(got) != "v1" {
		t.Errorf("restored = %q, %v", got, err)
	}
	// Only files SaveSnapshot wrote, as it left them, are received.
	forged := WriteFileInput{Path: "/srv/racksail/backups/forged.btrfs", Content: []byte("not a stream"), Mode: 0o600}
	if _, err := WriteFile.Call(ctx, c, forged, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreSnapshot.Call(ctx, c, RestoreSnapshotInput{File: forged.Path, Dir: restore.Dir}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("a file SaveSnapshot didn't write: %v", err)
	}
	if _, err := SaveSnapshot.Call(ctx, c, SaveSnapshotInput{Snapshot: snap, Dest: forged.Path}, CallOptions{}); !errors.Is(err, ErrConflict) {
		t.Errorf("saving over a file the agent didn't save: %v", err)
	}
	if err := b.Files.Chmod(ctx, save.Dest, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreSnapshot.Call(ctx, c, restore, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("a saved file others can read: %v", err)
	}
	if err := b.Files.Chmod(ctx, save.Dest, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := b.Files.WriteFile(ctx, save.Dest, []byte("{}\n"), 0o600, system.Owner{}); err != nil {
		t.Fatal(err)
	}
	if _, err := RestoreSnapshot.Call(ctx, c, restore, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("a saved file changed afterwards: %v", err)
	}
	if _, err := SaveSnapshot.Call(ctx, c, SaveSnapshotInput{Snapshot: blog, Dest: "/srv/racksail/backups/rw.btrfs"}, CallOptions{}); err == nil {
		t.Error("a writable subvolume can't be sent")
	}
	if _, err := b.Files.Stat(ctx, "/srv/racksail/backups/rw.btrfs"); !errors.Is(err, system.ErrNotFound) {
		t.Errorf("a failed save must leave no file: %v", err)
	}
}

func TestStreamsLocallyAndOverHTTP(t *testing.T) {
	ctx := context.Background()
	m := sim.New(sim.Options{Hostname: "streams"})
	srv := NewServer(m.Backend(), "test", nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	local := NewLocalRuntime(srv.Runtime())
	for name, s := range map[string]Streamer{"local": local, "http": NewHTTPClient(ts.URL, ts.Client())} {
		path := "/srv/racksail/" + name + ".bin"
		content := bytes.Repeat([]byte(name), 1<<16)
		if err := s.Upload(ctx, UploadInput{Path: path, Mode: 0o640}, bytes.NewReader(content)); err != nil {
			t.Fatalf("%s: upload: %v", name, err)
		}
		var got bytes.Buffer
		if err := s.Download(ctx, path, &got); err != nil || !bytes.Equal(got.Bytes(), content) {
			t.Errorf("%s: download = %d bytes, %v", name, got.Len(), err)
		}
		if err := s.Download(ctx, "/etc/shadow", &got); !errors.Is(err, ErrInvalidInput) {
			t.Errorf("%s: a path outside the policy: %v", name, err)
		}
		if err := s.Download(ctx, "/srv/racksail/missing", &got); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: a missing file: %v", name, err)
		}
	}
	records, _ := srv.Runtime().Audit().Recent(AuditFilter{Op: "streams/Upload"})
	if len(records) != 2 || !records[0].Changed {
		t.Errorf("uploads must be audited: %v", records)
	}
}

// Streams exist for what is too large for an operation's JSON, so the
// limit on operations' bodies doesn't apply to them.
func TestStreamsOutgrowTheOperationsLimit(t *testing.T) {
	ctx := context.Background()
	m := sim.New(sim.Options{Hostname: "big"})
	srv := NewServer(m.Backend(), "test", nil)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()
	client := NewHTTPClient(ts.URL, ts.Client())
	size := int64(maxBody) + 8<<20
	in := UploadInput{Path: "/srv/racksail/big.bin", Mode: 0o600}
	if err := client.Upload(ctx, in, io.LimitReader(repeatReader('r'), size)); err != nil {
		t.Fatalf("uploading %d bytes: %v", size, err)
	}
	info, err := m.Backend().Files.Stat(ctx, in.Path)
	if err != nil || info.Size != size {
		t.Fatalf("uploaded file = %+v, %v", info, err)
	}
	var got countingWriter
	if err := client.Download(ctx, in.Path, &got); err != nil || got.n != size {
		t.Errorf("downloading it = %d bytes, %v", got.n, err)
	}
	// Receiving reads the whole body too; this one isn't a send stream.
	err = client.ReceiveSnapshot(ctx, "/srv/racksail", io.LimitReader(repeatReader('x'), size))
	if !errors.Is(err, ErrInvalidInput) || strings.Contains(err.Error(), "too large") {
		t.Errorf("a large body that isn't a send stream: %v", err)
	}
	// Operations keep their limit.
	_, err = client.Call(ctx, "WriteFile", bytes.Repeat([]byte(" "), maxBody+1), CallOptions{})
	if !errors.Is(err, i18n.ErrTooLarge) {
		t.Errorf("an operation over the limit: %v", err)
	}
}

type repeatReader byte

func (r repeatReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = byte(r)
	}
	return len(p), nil
}

type countingWriter struct{ n int64 }

func (w *countingWriter) Write(p []byte) (int, error) {
	w.n += int64(len(p))
	return len(p), nil
}

// btrfs receive finds the parent of an incremental stream by UUID anywhere
// on the filesystem, so a stream may only build on a subvolume Racksail
// reads, never on a snapshot of the system.
func TestReceivedStreamsStayWithinWhatRacksailReads(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	b := m.Backend()
	blog := "/srv/racksail/accounts/blog"
	if _, err := EnsureSubvolume.Call(ctx, c, SubvolumeInput{Path: blog}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureDir.Call(ctx, c, EnsureDirInput{Path: SnapshotDir, Mode: 0o700}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"blog-1", "blog-2"} {
		if _, err := WriteFile.Call(ctx, c, WriteFileInput{Path: blog + "/index.html", Content: []byte(name), Mode: 0o644}, CallOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err := CreateSnapshot.Call(ctx, c, SnapshotInput{Source: blog, Dest: SnapshotDir + "/" + name, ReadOnly: true}, CallOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	var full, incremental bytes.Buffer
	if err := c.SendSnapshot(ctx, SnapshotStreamInput{Snapshot: SnapshotDir + "/blog-1"}, &full); err != nil {
		t.Fatal(err)
	}
	if err := c.SendSnapshot(ctx, SnapshotStreamInput{Snapshot: SnapshotDir + "/blog-2", Parent: SnapshotDir + "/blog-1"}, &incremental); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureSubvolume.Call(ctx, c, SubvolumeInput{Path: "/srv/racksail/replica"}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.ReceiveSnapshot(ctx, "/srv/racksail/replica", &full); err != nil {
		t.Fatalf("a full stream: %v", err)
	}
	if err := c.ReceiveSnapshot(ctx, "/srv/racksail/replica", &incremental); err != nil {
		t.Fatalf("an incremental stream on a snapshot Racksail reads: %v", err)
	}
	if got, _ := b.Files.ReadFile(ctx, "/srv/racksail/replica/blog-2/index.html", 0); string(got) != "blog-2" {
		t.Errorf("replica = %q", got)
	}

	// A stream claiming a snapshot of the system as its parent would hand
	// out a copy of everything root keeps.
	snaps, err := SnapshotPaths.Call(ctx, c, SnapshotPathsInput{Label: "chg_01x", Paths: []string{"/etc/racksail/generated/a.conf"}}, CallOptions{})
	if err != nil || len(snaps.Snapshots) != 1 {
		t.Fatalf("snapshots = %+v, %v", snaps, err)
	}
	svs, _ := b.Btrfs.Subvolumes(ctx, "/.snapshots")
	var rootUUID string
	for _, sv := range svs {
		if sv.Path == snaps.Snapshots[0].Path {
			rootUUID = sv.UUID
		}
	}
	forged := `{"format":"racksail-sim-send/1","name":"stolen","parent":"root","parentUuid":"` + rootUUID + `"}` + "\n"
	err = c.ReceiveSnapshot(ctx, "/srv/racksail/replica", strings.NewReader(forged))
	if !errors.Is(err, ErrInvalidInput) || !strings.Contains(err.Error(), "doesn't read") {
		t.Errorf("a stream building on the system's snapshot: %v", err)
	}
	if _, err := b.Files.Stat(ctx, "/srv/racksail/replica/stolen"); !errors.Is(err, system.ErrNotFound) {
		t.Errorf("a refused stream leaves nothing behind: %v", err)
	}
	unknown := `{"format":"racksail-sim-send/1","name":"orphan","parentUuid":"00000000-0000-4000-8000-000000000000"}` + "\n"
	if err := c.ReceiveSnapshot(ctx, "/srv/racksail/replica", strings.NewReader(unknown)); !errors.Is(err, ErrNotFound) {
		t.Errorf("a stream building on nothing: %v", err)
	}
	records, _ := c.Runtime().Audit().Recent(AuditFilter{Op: "streams/ReceiveSnapshot", Failed: true})
	if len(records) != 2 {
		t.Errorf("refused streams are audited: %+v", records)
	}
}

func TestObservation(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	if _, err := RestartUnit.Call(ctx, c, UnitInput{Name: "systemd-timesyncd.service"}, CallOptions{}); err != nil {
		t.Fatal(err)
	}
	page, err := ReadJournal.Call(ctx, c, ReadJournalInput{Query: system.JournalQuery{Units: []string{"systemd-timesyncd.service"}, Limit: 5, Newest: true}}, CallOptions{})
	if err != nil || len(page.Entries) == 0 || page.Cursor == "" {
		t.Fatalf("journal = %+v, %v", page, err)
	}
	for _, e := range page.Entries {
		if e.Unit != "systemd-timesyncd.service" {
			t.Errorf("entry of another unit: %+v", e)
		}
	}
	if _, err := ReadJournal.Call(ctx, c, ReadJournalInput{Query: system.JournalQuery{Units: []string{"not a unit"}}}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("a bad unit name: %v", err)
	}

	metrics, err := GetMetrics.Call(ctx, c, MetricsInput{Units: []string{system.PlatformSlice}}, CallOptions{})
	if err != nil || metrics.System.CPUs == 0 || metrics.System.Memory.TotalBytes == 0 || len(metrics.Units) != 1 {
		t.Fatalf("metrics = %+v, %v", metrics, err)
	}

	values, err := GetSysctl.Call(ctx, c, SysctlKeysInput{Keys: []string{"net.core.somaxconn", "net.core.somaxconn", "net.no.such.key"}}, CallOptions{})
	if err != nil || values.Values["net.core.somaxconn"] == "" || len(values.Values) != 1 {
		t.Errorf("sysctl = %+v, %v", values, err)
	}
	if _, err := GetSysctl.Call(ctx, c, SysctlKeysInput{}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("no keys: %v", err)
	}
	_ = m
}

// testSigningKey is a vendor key registered once for the tests.
var testSigningKey = func() *openpgp.Entity {
	e, err := openpgp.NewEntity("Runtime Vendor", "", "release@vendor.test", nil)
	if err != nil {
		panic(err)
	}
	var b bytes.Buffer
	w, _ := armor.Encode(&b, openpgp.PublicKeyType, nil)
	e.Serialize(w)
	w.Close()
	RegisterSigningKey("vendor-test", b.Bytes())
	return e
}()

func TestSignedVendorArchives(t *testing.T) {
	ctx := context.Background()
	m, c := newLocal(t)
	b := m.Backend()

	var raw bytes.Buffer
	gz := gzip.NewWriter(&raw)
	tw := tar.NewWriter(gz)
	bin := []byte("#!/bin/sh\necho runtime\n")
	tw.WriteHeader(&tar.Header{Name: "runtime-2.0/bin/run", Mode: 0o755, Size: int64(len(bin)), Typeflag: tar.TypeReg})
	tw.Write(bin)
	tw.Close()
	gz.Close()
	archive := raw.Bytes()
	sum := sha256.Sum256(archive)
	checksums := []byte(hex.EncodeToString(sum[:]) + "  runtime-2.0.tar.gz\n")
	var sig bytes.Buffer
	if err := openpgp.ArmoredDetachSign(&sig, testSigningKey, bytes.NewReader(checksums), nil); err != nil {
		t.Fatal(err)
	}
	m.SetDownload("https://vendor.test/runtime-2.0.tar.gz", archive)
	m.SetDownload("https://vendor.test/SHASUMS256.txt", checksums)
	m.SetDownload("https://vendor.test/SHASUMS256.txt.asc", sig.Bytes())

	in := InstallArchiveInput{Archive: system.Archive{
		URL: "https://vendor.test/runtime-2.0.tar.gz", Size: int64(len(archive)), Format: "tar.gz",
		Dest: system.RuntimesDir + "/vendor/2.0", StripComponents: 1,
		Signature: &system.ArchiveSignature{Key: "vendor-test", URL: "https://vendor.test/SHASUMS256.txt.asc", Checksums: "https://vendor.test/SHASUMS256.txt"},
	}}
	if res, err := InstallArchive.Call(ctx, c, in, CallOptions{}); err != nil || !res.Changed {
		t.Fatalf("install = %+v, %v", res, err)
	}
	got, err := b.Files.ReadFile(ctx, system.RuntimesDir+"/vendor/2.0/bin/run", 0)
	if err != nil || !bytes.Equal(got, bin) {
		t.Errorf("unpacked = %q, %v", got, err)
	}

	unknown := in
	unknownSig := *in.Archive.Signature
	unknownSig.Key = "someone-else"
	unknown.Archive.Signature = &unknownSig
	unknown.Archive.Dest = system.RuntimesDir + "/vendor/2.1"
	if _, err := InstallArchive.Call(ctx, c, unknown, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("a key the binary doesn't know: %v", err)
	}

	// A list signed by the right key that doesn't vouch for this archive.
	tampered := in
	tampered.Archive.Dest = system.RuntimesDir + "/vendor/2.2"
	m.SetDownload("https://vendor.test/runtime-2.0.tar.gz", append(slices.Clone(archive), 0))
	tampered.Archive.Size++
	if _, err := InstallArchive.Call(ctx, c, tampered, CallOptions{}); err == nil {
		t.Error("an archive the signed list doesn't match must be refused")
	}
	if _, err := b.Files.Stat(ctx, tampered.Archive.Dest); !errors.Is(err, system.ErrNotFound) {
		t.Errorf("nothing may be unpacked from a refused archive: %v", err)
	}
}
