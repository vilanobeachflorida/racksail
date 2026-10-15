// Command release does the parts of publishing a release that need to agree
// with how servers read releases: which channel a version belongs to, what
// a nightly build is called, which release an upgrade test starts from, the
// checksums file and the release manifest.
//
//	go run ./packaging/release channel v1.3.0-beta.1
//	go run ./packaging/release nightly-version -date 20261015 -run 42
//	git tag --list 'v*' | go run ./packaging/release previous -version 1.3.0
//	go run ./packaging/release checksums -dir dist
//	go run ./packaging/release manifest -dir dist -tag v1.2.3 -base-url URL
//	go run ./packaging/release check-pins [-write]
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/updates"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "release:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: release channel | nightly-version | previous | checksums | manifest | check-pins")
	}
	cmd, args := args[0], args[1:]
	flags := flag.NewFlagSet(cmd, flag.ContinueOnError)
	switch cmd {
	case "channel":
		if len(args) != 1 {
			return errors.New("usage: release channel VERSION")
		}
		ch, err := Channel(args[0])
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, ch)
		return nil

	case "nightly-version":
		date := flags.String("date", time.Now().UTC().Format("20060102"), "the build's day, as YYYYMMDD")
		runNumber := flags.Int("run", 0, "the CI run number, which tells builds of one day apart")
		if err := flags.Parse(args); err != nil {
			return err
		}
		out, err := exec.CommandContext(ctx, "git", "tag", "--list", "v*").Output()
		if err != nil {
			return fmt.Errorf("listing tags: %w", err)
		}
		v, err := NightlyVersion(strings.Fields(string(out)), *date, *runNumber)
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, v)
		return nil

	case "previous":
		version := flags.String("version", "", "the version being released")
		channel := flags.String("channel", "", "the channel of the servers that upgrade; the version's own by default")
		if err := flags.Parse(args); err != nil {
			return err
		}
		if *version == "" {
			return errors.New("previous needs -version, and the release tags on its input")
		}
		ch := updates.Channel(*channel)
		if *channel == "" {
			var err error
			if ch, err = Channel(*version); err != nil {
				return err
			}
		}
		tags, err := io.ReadAll(io.LimitReader(stdin, 1<<20))
		if err != nil {
			return err
		}
		tag, err := Previous(strings.Fields(string(tags)), ch, *version)
		if err != nil {
			return err
		}
		if tag != "" {
			fmt.Fprintln(stdout, tag)
		}
		return nil

	case "checksums":
		dir := flags.String("dir", "dist", "the release's directory")
		if err := flags.Parse(args); err != nil {
			return err
		}
		data, err := Checksums(*dir)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(*dir, ChecksumsFile), data, 0o644)

	case "manifest":
		dir := flags.String("dir", "dist", "the release's directory, with every artifact signed")
		tag := flags.String("tag", "", "the release's tag, such as v1.2.3")
		base := flags.String("base-url", "", "where the artifacts are downloaded from")
		notes := flags.String("notes", "", "the release notes' address")
		published := flags.String("time", "", "when the release is published, in RFC 3339; now by default")
		if err := flags.Parse(args); err != nil {
			return err
		}
		if *tag == "" || *base == "" {
			return errors.New("manifest needs -tag and -base-url")
		}
		when := time.Now()
		if *published != "" {
			t, err := time.Parse(time.RFC3339, *published)
			if err != nil {
				return err
			}
			when = t
		}
		rel, err := Manifest(*dir, *tag, *base, *notes, when)
		if err != nil {
			return err
		}
		data, err := json.MarshalIndent(rel, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(*dir, ManifestFile), append(data, '\n'), 0o644)

	case "check-pins":
		script := flags.String("script", "packaging/install.sh", "the install script")
		write := flags.Bool("write", false, "write cosign's published checksums into the script")
		if err := flags.Parse(args); err != nil {
			return err
		}
		data, err := os.ReadFile(*script)
		if err != nil {
			return err
		}
		version, sums, err := CosignPins(data)
		if err != nil {
			return err
		}
		published, err := fetch(ctx, "https://github.com/sigstore/cosign/releases/download/"+version+"/cosign_checksums.txt")
		if err != nil {
			return err
		}
		if *write {
			updated, err := UpdateCosignPins(data, published)
			if err != nil {
				return err
			}
			return os.WriteFile(*script, updated, 0o755)
		}
		if err := CheckCosignPins(sums, published); err != nil {
			return fmt.Errorf("%w; run go run ./packaging/release check-pins -write", err)
		}
		fmt.Fprintf(stdout, "the install script's cosign %s checksums match the release\n", version)
		return nil
	}
	return fmt.Errorf("unknown command %q", cmd)
}

func fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}
