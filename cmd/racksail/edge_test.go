package main

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/edge"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// The edge takes the admin socket systemd passes in. The test plays systemd:
// it creates the socket and starts the edge, in a process of its own, with
// the socket as file descriptor 3 and LISTEN_FDS set, as socket activation
// does.
func TestEdgeTakesTheSocketSystemdPasses(t *testing.T) {
	if config := os.Getenv("RACKSAIL_TEST_EDGE_CONFIG"); config != "" {
		// systemd sets LISTEN_PID to the process it starts; this is that
		// process.
		os.Setenv("LISTEN_PID", strconv.Itoa(os.Getpid()))
		root := newRootCommand()
		// Were the edge to create a socket of its own, it would be in own.
		own := filepath.Join(filepath.Dir(config), "own", "edge.sock")
		root.SetArgs([]string{"edge", "--config", config, "--state", filepath.Join(filepath.Dir(config), "state"), "--socket", own})
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		return
	}
	dir := shortTemp(t)
	sock := filepath.Join(dir, "edge.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	file, err := ln.(*net.UnixListener).File()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	own := filepath.Join(dir, "own", "edge.sock")
	config := filepath.Join(dir, "racksail.toml")
	toml := "[edge]\nhttp = \"127.0.0.1:0\"\nhttps = \"127.0.0.1:0\"\n[log]\nformat = \"text\"\n"
	if err := os.WriteFile(config, []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	child := exec.Command(os.Args[0], "-test.run=^TestEdgeTakesTheSocketSystemdPasses$")
	child.Env = append(os.Environ(), "RACKSAIL_TEST_EDGE_CONFIG="+config, "LISTEN_FDS=1", "XDG_DATA_HOME="+dir)
	child.ExtraFiles = []*os.File{file}
	var output bytes.Buffer
	child.Stdout, child.Stderr = &output, &output
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		child.Process.Kill()
		child.Wait()
	}()

	// Nobody accepts on this process's copy of the socket, so an edge that
	// ignored it would leave requests waiting; each attempt has a deadline.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := edge.DialAdmin(sock)
	var routes []edge.Route
	for start := time.Now(); ; time.Sleep(20 * time.Millisecond) {
		attempt, stop := context.WithTimeout(ctx, time.Second)
		routes, err = client.Routes(attempt)
		stop()
		if err == nil {
			break
		}
		if time.Since(start) > 5*time.Second {
			t.Fatalf("the edge never answered on the socket it was given: %v\n%s", err, output.String())
		}
	}
	if len(routes) != 0 {
		t.Errorf("a new edge has no routes: %+v", routes)
	}
	route := edge.Route{ID: "main", Hosts: []string{"example.test"}, Handler: edge.Handler{Type: edge.HandleRespond, Body: "hello"}}
	if err := client.SetRoutes(ctx, store.NewID("site"), []edge.Route{route}); err != nil {
		t.Fatal(err)
	}
	if routes, err = client.Routes(ctx); err != nil || len(routes) != 1 {
		t.Errorf("routes after setting one = %+v, %v", routes, err)
	}
	if _, err := os.Stat(filepath.Dir(own)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the edge made a socket of its own: %v", err)
	}
}

func TestEdgeCreatesItsOwnSocketWithoutSystemd(t *testing.T) {
	path := filepath.Join(shortTemp(t), "run", "edge.sock")
	ln, activated, err := adminListener(path, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if activated {
		t.Error("a socket the edge made isn't one systemd passed in")
	}
	if fi, err := os.Stat(path); err != nil || fi.Mode()&fs.ModeSocket == 0 || fi.Mode().Perm() != 0o600 {
		t.Errorf("the socket = %v, %v", fi, err)
	}
}

func shortTemp(t *testing.T) string {
	t.Helper()
	// Unix socket paths are limited to about 100 bytes, so avoid t.TempDir.
	dir, err := os.MkdirTemp("", "rs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}
