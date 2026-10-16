// Command tool checks a Racksail catalog and builds its signed index.
//
// The official catalog in catalog/ is checked on every change and published
// by CI; anyone running a third-party catalog uses the same tool to check,
// build and sign theirs. The catalog's items are licensed under MIT, but
// this tool is part of Racksail's code and licensed under AGPL-3.0-or-later
// like the rest of it.
//
// Usage:
//
//	go run ./catalog/tool validate [-dir catalog]
//	go run ./catalog/tool index -out DIR [-dir catalog] [-key FILE | -key-env NAME] [-public FILE] [-archive FILE]
//	go run ./catalog/tool verify -dir DIR -public FILE
//	go run ./catalog/tool keygen -private FILE -public FILE
//	go run ./catalog/tool pin [-write] [PATH...]
package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const usage = `Checks a Racksail catalog and builds its signed index.

Commands:
  validate   check every item against the schemas and the catalog's rules
  index      build the index and publish the catalog into a directory
  verify     check a published catalog's signature and files
  keygen     create a signing key pair
  pin        download what recipes list and check their checksums and sizes

Run "tool COMMAND -h" for a command's flags.
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		if errors.Is(err, errUsage) {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

var errUsage = errors.New("see the usage above")

func run(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, usage)
		return errUsage
	}
	cmd, args := args[0], args[1:]
	switch cmd {
	case "validate":
		return runValidate(args, stdout, stderr)
	case "index":
		return runIndex(args, stdout, stderr)
	case "verify":
		return runVerify(args, stdout, stderr)
	case "keygen":
		return runKeygen(args, stdout, stderr)
	case "pin":
		return runPin(ctx, args, stdout, stderr)
	case "-h", "-help", "--help", "help":
		fmt.Fprint(stdout, usage)
		return nil
	}
	fmt.Fprintf(stderr, "unknown command %q\n\n%s", cmd, usage)
	return errUsage
}

func newFlags(name string, stderr io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(stderr)
	return flags
}

func parse(flags *flag.FlagSet, args []string) error {
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return errUsage
	}
	return nil
}

func runValidate(args []string, stdout, stderr io.Writer) error {
	flags := newFlags("validate", stderr)
	dir := flags.String("dir", "catalog", "the catalog's directory")
	if err := parse(flags, args); err != nil {
		return err
	}
	c, err := Load(*dir)
	if err != nil {
		return fmt.Errorf("the catalog has problems:\n%w", err)
	}
	fmt.Fprintf(stdout, "%s: %s, no problems\n", *dir, plural(len(c.Items), "item"))
	return nil
}

func runIndex(args []string, stdout, stderr io.Writer) error {
	flags := newFlags("index", stderr)
	dir := flags.String("dir", "catalog", "the catalog's directory")
	out := flags.String("out", "", "directory to publish the catalog into; it must not exist or be empty")
	name := flags.String("name", "official", "the catalog's name, recorded in the index")
	keyFile := flags.String("key", "", "file holding the signing key")
	keyEnv := flags.String("key-env", "", "environment variable holding the signing key, instead of -key")
	publicFile := flags.String("public", "", "the catalog's public key; the signing key must match it")
	archive := flags.String("archive", "", "also pack the published catalog into this .tar.gz file")
	builtFlag := flags.String("time", "", "build time in RFC 3339; SOURCE_DATE_EPOCH or the current time otherwise")
	hideAfter := flags.Duration("hide-unmaintained-after", DefaultHideAfter, "leave out items unmaintained for longer than this")
	if err := parse(flags, args); err != nil {
		return err
	}
	if *out == "" {
		fmt.Fprintln(stderr, "index needs -out")
		return errUsage
	}
	built, err := buildTime(*builtFlag)
	if err != nil {
		return err
	}
	key, err := signingKey(*keyFile, *keyEnv)
	if err != nil {
		return err
	}
	if *publicFile != "" {
		if key == nil {
			return errors.New("-public checks the signing key, so it needs -key or -key-env")
		}
		data, err := os.ReadFile(*publicFile)
		if err != nil {
			return err
		}
		pub, err := ParsePublicKey(data)
		if err != nil {
			return err
		}
		if !pub.Equal(key.Public()) {
			return fmt.Errorf("the signing key doesn't belong to %s", *publicFile)
		}
	}
	c, err := Load(*dir)
	if err != nil {
		return fmt.Errorf("the catalog has problems:\n%w", err)
	}
	idx := BuildIndex(c, IndexOptions{Name: *name, Built: built, HideAfter: *hideAfter})
	if err := Publish(c, idx, *out, key); err != nil {
		return err
	}
	if *archive != "" {
		if err := Archive(*out, built, *archive); err != nil {
			return err
		}
	}
	signed := "unsigned"
	if key != nil {
		signed = "signed"
	}
	fmt.Fprintf(stdout, "%s: %d of %s, %s, built %s\n", filepath.Join(*out, indexFile), len(idx.Items), plural(len(c.Items), "item"), signed, idx.Built.Format(time.RFC3339))
	return nil
}

func plural(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// buildTime picks the time recorded in the index: the flag, then
// SOURCE_DATE_EPOCH as reproducible builds set it, then now.
func buildTime(flagValue string) (time.Time, error) {
	if flagValue != "" {
		t, err := time.Parse(time.RFC3339, flagValue)
		if err != nil {
			return time.Time{}, fmt.Errorf("-time: %w", err)
		}
		return t.UTC(), nil
	}
	if epoch := os.Getenv("SOURCE_DATE_EPOCH"); epoch != "" {
		secs, err := strconv.ParseInt(epoch, 10, 64)
		if err != nil {
			return time.Time{}, fmt.Errorf("SOURCE_DATE_EPOCH %q isn't a number of seconds", epoch)
		}
		return time.Unix(secs, 0).UTC(), nil
	}
	return time.Now().UTC().Truncate(time.Second), nil
}

func signingKey(file, env string) (ed25519.PrivateKey, error) {
	switch {
	case file != "" && env != "":
		return nil, errors.New("use -key or -key-env, not both")
	case file != "":
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		return ParsePrivateKey(data)
	case env != "":
		value := os.Getenv(env)
		if value == "" {
			return nil, fmt.Errorf("the environment variable %s is empty", env)
		}
		return ParsePrivateKey([]byte(value))
	}
	return nil, nil
}

func runVerify(args []string, stdout, stderr io.Writer) error {
	flags := newFlags("verify", stderr)
	dir := flags.String("dir", "", "a published catalog's directory")
	publicFile := flags.String("public", "", "the catalog's public key")
	if err := parse(flags, args); err != nil {
		return err
	}
	if *dir == "" || *publicFile == "" {
		fmt.Fprintln(stderr, "verify needs -dir and -public")
		return errUsage
	}
	data, err := os.ReadFile(*publicFile)
	if err != nil {
		return err
	}
	pub, err := ParsePublicKey(data)
	if err != nil {
		return err
	}
	idx, err := Verify(*dir, pub)
	if err != nil {
		return err
	}
	fmt.Fprintf(stdout, "%s: signature good, %s and their files match the index\n", *dir, plural(len(idx.Items), "item"))
	return nil
}

func runKeygen(args []string, stdout, stderr io.Writer) error {
	flags := newFlags("keygen", stderr)
	private := flags.String("private", "", "file to write the signing key to; it must not exist")
	public := flags.String("public", "", "file to write the public key to; it must not exist")
	if err := parse(flags, args); err != nil {
		return err
	}
	if *private == "" || *public == "" {
		fmt.Fprintln(stderr, "keygen needs -private and -public")
		return errUsage
	}
	if _, err := GenerateKeys(*private, *public); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "wrote the signing key to %s and the public key to %s\n", *private, *public)
	return nil
}

func runPin(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	flags := newFlags("pin", stderr)
	write := flags.Bool("write", false, "correct checksums and sizes in the recipes")
	timeout := flags.Duration("timeout", time.Hour, "how long all downloads may take")
	if err := parse(flags, args); err != nil {
		return err
	}
	paths := flags.Args()
	if len(paths) == 0 {
		paths = []string{"catalog"}
	}
	files, err := recipeFiles(paths)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, *timeout)
	defer cancel()
	return pinFiles(ctx, http.DefaultClient, files, *write, stdout)
}

func pinFiles(ctx context.Context, client *http.Client, files []string, write bool, stdout io.Writer) error {
	differs := 0
	for _, file := range files {
		results, err := Pin(ctx, client, file, write)
		if err != nil {
			return err
		}
		for _, r := range results {
			switch {
			case !r.Changed:
				fmt.Fprintf(stdout, "%s: %s matches\n", file, r.URL)
			case write:
				fmt.Fprintf(stdout, "%s: %s corrected to sha256 %s, %d bytes\n", file, r.URL, r.SHA256, r.Size)
			default:
				differs++
				fmt.Fprintf(stdout, "%s: %s is sha256 %s, %d bytes, which the recipe doesn't say\n", file, r.URL, r.SHA256, r.Size)
			}
		}
	}
	if differs > 0 {
		return fmt.Errorf("%d downloads don't match their recipes; run pin -write to correct them", differs)
	}
	return nil
}

// recipeFiles expands directories into the recipes they hold.
func recipeFiles(paths []string) ([]string, error) {
	var out []string
	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			out = append(out, p)
			continue
		}
		err = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			// Fixtures in the tool's testdata aren't the catalog's recipes.
			if d.IsDir() && path != p && (strings.HasPrefix(d.Name(), ".") || d.Name() == "testdata") {
				return filepath.SkipDir
			}
			if !d.IsDir() && d.Name() == recipeFile {
				out = append(out, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}
