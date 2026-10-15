package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/system"
	"github.com/vilanobeachflorida/racksail/internal/system/linux"
)

// idleSystemd is a systemd with no units loaded, for tests of the real
// adapters that never start anything.
type idleSystemd struct{}

func (idleSystemd) DaemonReload(context.Context) error               { return nil }
func (idleSystemd) Start(context.Context, string) error              { return nil }
func (idleSystemd) Stop(context.Context, string) error               { return nil }
func (idleSystemd) Restart(context.Context, string) error            { return nil }
func (idleSystemd) Reload(context.Context, string) error             { return nil }
func (idleSystemd) Enable(context.Context, []string) error           { return nil }
func (idleSystemd) Disable(context.Context, []string) error          { return nil }
func (idleSystemd) Kill(context.Context, string, int32) error        { return nil }
func (idleSystemd) ResetFailed(context.Context, string) error        { return nil }
func (idleSystemd) List(context.Context, []string) ([]string, error) { return nil, nil }
func (idleSystemd) Properties(context.Context, string) (map[string]any, error) {
	return map[string]any{"LoadState": "not-found", "ActiveState": "inactive", "SubState": "dead"}, nil
}

// linuxBackend returns the real adapters for a machine root.
func linuxBackend(root string) *system.Backend {
	return linux.NewWith(linux.Options{Root: root, Systemd: idleSystemd{}})
}

// linuxRoot builds a machine root for the real adapters in a temporary
// directory, with root mapped to the user running the tests so ownership
// changes need no privileges.
func linuxRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	uid, gid := os.Getuid(), os.Getgid()
	files := map[string]string{
		"etc/passwd":                             fmt.Sprintf("root:x:%d:%d:root:/root:/bin/bash\n", uid, gid),
		"etc/group":                              fmt.Sprintf("root:x:%d:\n", gid),
		"etc/outside/secret.txt":                 "root's secret",
		"srv/racksail/accounts/a/www/index.html": "a's page",
	}
	for name, content := range files {
		p := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// An account controls its own directory, so it can put a link anywhere in
// it. Operations the agent does as root must never go through one.
func TestOperationsNeverFollowAnAccountsLinks(t *testing.T) {
	ctx := context.Background()
	root := linuxRoot(t)
	if err := os.Symlink(filepath.Join(root, "etc/outside"), filepath.Join(root, "srv/racksail/accounts/a/out")); err != nil {
		t.Fatal(err)
	}
	c := NewLocal(linuxBackend(root), nil)
	through := "/srv/racksail/accounts/a/out/secret.txt"

	if out, err := ReadFile.Call(ctx, c, ReadFileInput{Path: through}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("ReadFile = %q, %v", out.Content, err)
	}
	if out, err := StatPath.Call(ctx, c, PathInput{Path: through}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("StatPath = %+v, %v", out, err)
	}
	if out, err := ListDir.Call(ctx, c, PathInput{Path: "/srv/racksail/accounts/a/out"}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("ListDir = %+v, %v", out, err)
	}
	var got bytes.Buffer
	if err := c.Download(ctx, through, &got); !errors.Is(err, ErrInvalidInput) || got.Len() > 0 {
		t.Errorf("Download = %q, %v", got.String(), err)
	}
	if _, err := WriteFile.Call(ctx, c, WriteFileInput{Path: "/srv/racksail/accounts/a/out/planted", Content: []byte("x"), Mode: 0o644}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("WriteFile: %v", err)
	}
	if err := c.Upload(ctx, UploadInput{Path: "/srv/racksail/accounts/a/out/uploaded", Mode: 0o644}, bytes.NewReader([]byte("x"))); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("Upload: %v", err)
	}
	if _, err := EnsureDir.Call(ctx, c, EnsureDirInput{Path: "/srv/racksail/accounts/a/out/dir", Mode: 0o755}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("EnsureDir: %v", err)
	}
	if _, err := RemovePath.Call(ctx, c, RemovePathInput{Path: through}, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("RemovePath: %v", err)
	}
	archive := CreateArchiveInput{Root: "/srv/racksail/accounts/a/out", Source: ".", Dest: "copy.tar", Format: system.FormatTar, Mode: 0o600}
	if _, err := CreateArchive.Call(ctx, c, archive, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("CreateArchive on a linked root: %v", err)
	}
	extract := ExtractArchiveInput{Root: "/srv/racksail/accounts/a/out", Archive: "x.tar", Dest: "x", Format: system.FormatTar}
	if _, err := ExtractArchive.Call(ctx, c, extract, CallOptions{}); !errors.Is(err, ErrInvalidInput) {
		t.Errorf("ExtractArchive on a linked root: %v", err)
	}
	for _, leftover := range []string{"etc/outside/planted", "etc/outside/uploaded", "etc/outside/dir", "etc/outside/copy.tar"} {
		if _, err := os.Lstat(filepath.Join(root, leftover)); err == nil {
			t.Errorf("%s was written through the link", leftover)
		}
	}
	if data, _ := os.ReadFile(filepath.Join(root, "etc/outside/secret.txt")); string(data) != "root's secret" {
		t.Errorf("the file behind the link = %q", data)
	}

	// The same operations work where nothing is linked.
	page := "/srv/racksail/accounts/a/www/index.html"
	if out, err := ReadFile.Call(ctx, c, ReadFileInput{Path: page}, CallOptions{}); err != nil || string(out.Content) != "a's page" {
		t.Errorf("ReadFile = %q, %v", out.Content, err)
	}
	if _, err := WriteFile.Call(ctx, c, WriteFileInput{Path: "/srv/racksail/accounts/a/www/about.html", Content: []byte("about"), Mode: 0o644}, CallOptions{}); err != nil {
		t.Errorf("WriteFile: %v", err)
	}
	pack := CreateArchiveInput{Root: "/srv/racksail/accounts/a", Source: "www", Dest: "www.tar", Format: system.FormatTar, Mode: 0o600}
	if res, err := CreateArchive.Call(ctx, c, pack, CallOptions{}); err != nil || res.Stats.Files != 2 {
		t.Errorf("CreateArchive = %+v, %v", res, err)
	}
	got.Reset()
	if err := c.Download(ctx, page, &got); err != nil || got.String() != "a's page" {
		t.Errorf("Download = %q, %v", got.String(), err)
	}
}
