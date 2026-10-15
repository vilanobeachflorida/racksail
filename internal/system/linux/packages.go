package linux

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

var (
	packageNamePattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9.+-]+$`)
	packageVersionPattern = regexp.MustCompile(`^[A-Za-z0-9.+~:-]+$`)
	instLine              = regexp.MustCompile(`^Inst (\S+) (?:\[(\S+)\] )?\((\S+) (.*?)\s*\[(\S+)\]\)`)
)

const aptTimeout = 30 * time.Minute

type packages struct{ h *host }

func checkPackages(names []string) error {
	for _, n := range names {
		if !packageNamePattern.MatchString(n) {
			return fmt.Errorf("package name %q: %w", n, system.ErrInvalid)
		}
	}
	return nil
}

func (p *packages) apt(ctx context.Context, args ...string) ([]byte, error) {
	out, err := p.h.run.Run(ctx, system.Command{Path: progAptGet, Args: args, Timeout: aptTimeout})
	return out.Stdout, err
}

func (p *packages) Installed(ctx context.Context, names ...string) ([]system.Package, error) {
	if len(names) == 0 {
		return nil, nil
	}
	if err := checkPackages(names); err != nil {
		return nil, err
	}
	args := append([]string{"--show", "--showformat=${Package}\t${Version}\t${Architecture}\t${Installed-Size}\t${db:Status-Status}\n", "--"}, names...)
	out, err := p.h.exec(ctx, progDpkgQuery, args...)
	// dpkg-query exits with status 1 when some names are unknown and still
	// lists the others.
	var exitErr *system.ExitError
	if err != nil && !(errors.As(err, &exitErr) && exitErr.ExitCode == 1) {
		return nil, fmt.Errorf("querying installed packages: %w", err)
	}
	return parseDpkgQuery(out), nil
}

func parseDpkgQuery(out []byte) []system.Package {
	var pkgs []system.Package
	eachLine(out, func(line string) {
		f := strings.Split(line, "\t")
		if len(f) != 5 || f[4] != "installed" {
			return
		}
		pkgs = append(pkgs, system.Package{Name: f[0], Version: f[1], Arch: f[2], InstalledBytes: atoi64(f[3]) * 1024})
	})
	return pkgs
}

func (p *packages) Candidates(ctx context.Context, names ...string) ([]system.Package, error) {
	if err := checkPackages(names); err != nil {
		return nil, err
	}
	sim, err := p.apt(ctx, append([]string{"install", "--simulate", "-q", "--no-install-recommends", "--"}, names...)...)
	if err != nil {
		return nil, fmt.Errorf("resolving %s: %w", strings.Join(names, " "), err)
	}
	pkgs := parseInst(sim)
	if len(pkgs) == 0 {
		return nil, nil
	}
	show := []string{"show", "--no-all-versions", "--"}
	for _, pkg := range pkgs {
		show = append(show, pkg.Name)
	}
	info, err := p.h.exec(ctx, progAptCache, show...)
	if err != nil {
		return nil, fmt.Errorf("reading package sizes: %w", err)
	}
	sizes := parseAptShow(info)
	for i := range pkgs {
		if s, ok := sizes[pkgs[i].Name]; ok {
			pkgs[i].DownloadBytes, pkgs[i].InstalledBytes = s.DownloadBytes, s.InstalledBytes
		}
	}
	return pkgs, nil
}

// parseInst reads the "Inst" lines of a simulated apt-get run.
func parseInst(out []byte) []system.Package {
	var pkgs []system.Package
	eachLine(out, func(line string) {
		m := instLine.FindStringSubmatch(line)
		if m == nil {
			return
		}
		pkgs = append(pkgs, system.Package{Name: m[1], Version: m[3], Origin: strings.TrimSpace(m[4]), Arch: m[5]})
	})
	return pkgs
}

// parseAptShow reads apt-cache show stanzas for their sizes.
func parseAptShow(out []byte) map[string]system.Package {
	res := map[string]system.Package{}
	for _, stanza := range bytes.Split(out, []byte("\n\n")) {
		var pkg system.Package
		eachLine(stanza, func(line string) {
			key, value, ok := strings.Cut(line, ":")
			if !ok {
				return
			}
			value = strings.TrimSpace(value)
			switch key {
			case "Package":
				pkg.Name = value
			case "Version":
				pkg.Version = value
			case "Size":
				pkg.DownloadBytes = atoi64(value)
			case "Installed-Size":
				pkg.InstalledBytes = atoi64(value) * 1024
			}
		})
		if pkg.Name != "" {
			res[pkg.Name] = pkg
		}
	}
	return res
}

func (p *packages) UpdateIndex(ctx context.Context) error {
	if _, err := p.apt(ctx, "update", "-q"); err != nil {
		return fmt.Errorf("updating the package index: %w", err)
	}
	return nil
}

func (p *packages) Install(ctx context.Context, pkgs ...system.PackageRequest) error {
	args := []string{"install", "-y", "-q", "--no-install-recommends",
		"-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold", "--"}
	for _, r := range pkgs {
		if !packageNamePattern.MatchString(r.Name) {
			return fmt.Errorf("package name %q: %w", r.Name, system.ErrInvalid)
		}
		if r.Version == "" {
			args = append(args, r.Name)
			continue
		}
		if !packageVersionPattern.MatchString(r.Version) {
			return fmt.Errorf("package version %q: %w", r.Version, system.ErrInvalid)
		}
		args = append(args, r.Name+"="+r.Version)
	}
	if _, err := p.apt(ctx, args...); err != nil {
		return fmt.Errorf("installing packages: %w", err)
	}
	return nil
}

func (p *packages) Remove(ctx context.Context, purge bool, names ...string) error {
	if err := checkPackages(names); err != nil {
		return err
	}
	verb := "remove"
	if purge {
		verb = "purge"
	}
	if _, err := p.apt(ctx, append([]string{verb, "-y", "-q", "--"}, names...)...); err != nil {
		return fmt.Errorf("removing packages: %w", err)
	}
	return nil
}

func (p *packages) Upgradable(ctx context.Context) ([]system.Package, error) {
	out, err := p.apt(ctx, "upgrade", "--simulate", "-q")
	if err != nil {
		return nil, fmt.Errorf("listing upgrades: %w", err)
	}
	pkgs := parseInst(out)
	sort.Slice(pkgs, func(i, j int) bool { return pkgs[i].Name < pkgs[j].Name })
	return pkgs, nil
}

func (p *packages) AddSource(ctx context.Context, src system.AptSource) error {
	rendered, err := src.Render()
	if err != nil {
		return err
	}
	f := &files{p.h}
	// The key and the pin go first, so apt never sees the source without them.
	for _, file := range []struct {
		path string
		data []byte
	}{{rendered.KeyPath, rendered.Key}, {rendered.PrefsPath, rendered.Prefs}, {rendered.SourcesPath, rendered.Sources}} {
		if err := os.MkdirAll(p.h.path(path.Dir(file.path)), 0o755); err != nil {
			return mapErr("add source", file.path, err)
		}
		if err := f.WriteFile(ctx, file.path, file.data, 0o644, system.Owner{}); err != nil {
			return err
		}
	}
	return nil
}

var sourceNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,62}$`)

func (p *packages) RemoveSource(ctx context.Context, name string) error {
	if !sourceNamePattern.MatchString(name) {
		return fmt.Errorf("apt source name %q: %w", name, system.ErrInvalid)
	}
	stem := "racksail-" + name
	f := &files{p.h}
	if err := f.Remove(ctx, path.Join(system.AptSourceDir, stem+".sources")); err != nil {
		return err
	}
	for _, extra := range []string{path.Join(system.AptKeyDir, stem+".asc"), path.Join(system.AptPrefsDir, stem+".pref")} {
		if err := f.Remove(ctx, extra); err != nil && !errors.Is(err, system.ErrNotFound) {
			return err
		}
	}
	return nil
}

func (p *packages) Sources(ctx context.Context) ([]string, error) {
	entries, err := os.ReadDir(p.h.path(system.AptSourceDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		name, ok := strings.CutPrefix(e.Name(), "racksail-")
		if ok && strings.HasSuffix(name, ".sources") {
			out = append(out, strings.TrimSuffix(name, ".sources"))
		}
	}
	sort.Strings(out)
	return out, nil
}

func (p *packages) InstallArchive(ctx context.Context, a system.Archive) error {
	if err := a.Validate(); err != nil {
		return err
	}
	dest := p.h.path(a.Dest)
	if _, err := os.Lstat(dest); err == nil {
		return nil
	}
	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return mapErr("install archive", a.Dest, err)
	}
	download, err := os.CreateTemp(parent, ".racksail-download-*")
	if err != nil {
		return mapErr("install archive", a.Dest, err)
	}
	defer os.Remove(download.Name())
	defer download.Close()
	if err := p.fetch(ctx, a, download); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, ".racksail-unpack-*")
	if err != nil {
		return mapErr("install archive", a.Dest, err)
	}
	defer os.RemoveAll(staging)
	switch a.Format {
	case "tar.gz":
		err = extractTarGz(download, staging, a.StripComponents)
	case "zip":
		err = extractZip(download, a.Size, staging, a.StripComponents)
	case "tar.xz":
		_, err = p.h.exec(ctx, progTar, "--extract", "--xz", "--file", download.Name(), "--directory", staging,
			"--strip-components="+strconv.Itoa(a.StripComponents), "--no-same-owner", "--no-same-permissions")
	}
	if err != nil {
		return fmt.Errorf("unpacking %s: %w", a.URL, err)
	}
	if err := os.Chmod(staging, 0o755); err != nil {
		return err
	}
	return mapErr("install archive", a.Dest, os.Rename(staging, dest))
}

// fetch downloads the archive into w, refusing anything bigger than
// expected and anything whose checksum doesn't match.
func (p *packages) fetch(ctx context.Context, a system.Archive, w io.WriteSeeker) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.URL, nil)
	if err != nil {
		return err
	}
	resp, err := p.h.http.Do(req)
	if err != nil {
		return fmt.Errorf("downloading %s: %w", a.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("downloading %s: %s", a.URL, resp.Status)
	}
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, sum), io.LimitReader(resp.Body, a.Size+1))
	if err != nil {
		return fmt.Errorf("downloading %s: %w", a.URL, err)
	}
	if n > a.Size {
		return fmt.Errorf("%s is larger than the expected %d bytes: %w", a.URL, a.Size, system.ErrTooLarge)
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != a.SHA256 {
		return fmt.Errorf("%s has checksum %s, expected %s: %w", a.URL, got, a.SHA256, system.ErrInvalid)
	}
	_, err = w.Seek(0, io.SeekStart)
	return err
}

// archivePath maps an archive member to a path inside dir, or "" when the
// member is stripped away. Absolute names and names with ".." are refused
// rather than cleaned up: a vendor archive never needs them.
func archivePath(dir, name string, strip int) (string, error) {
	name = strings.ReplaceAll(name, `\`, "/")
	if path.IsAbs(name) || slices.Contains(strings.Split(name, "/"), "..") {
		return "", fmt.Errorf("archive member %q points outside the destination: %w", name, system.ErrInvalid)
	}
	name = strings.TrimPrefix(path.Clean("/"+name), "/")
	parts := strings.Split(name, "/")
	if name == "" || len(parts) <= strip {
		return "", nil
	}
	target := filepath.Join(dir, filepath.FromSlash(path.Join(parts[strip:]...)))
	if !strings.HasPrefix(target, dir+string(filepath.Separator)) {
		return "", fmt.Errorf("archive member %q points outside the destination: %w", name, system.ErrInvalid)
	}
	return target, nil
}

func checkLink(dir, target, link string) error {
	if path.IsAbs(link) {
		return fmt.Errorf("archive link to %q is absolute: %w", link, system.ErrInvalid)
	}
	resolved := filepath.Join(filepath.Dir(target), filepath.FromSlash(link))
	if resolved != dir && !strings.HasPrefix(resolved, dir+string(filepath.Separator)) {
		return fmt.Errorf("archive link to %q escapes the destination: %w", link, system.ErrInvalid)
	}
	return nil
}

func extractTarGz(r io.Reader, dir string, strip int) error {
	gz, err := gzip.NewReader(r)
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := archivePath(dir, hdr.Name, strip)
		if err != nil || target == "" {
			if err != nil {
				return err
			}
			continue
		}
		mode := fs.FileMode(hdr.Mode).Perm()
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := writeMember(target, tr, mode); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := checkLink(dir, target, hdr.Linkname); err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		default:
			return fmt.Errorf("archive member %q has an unsupported type: %w", hdr.Name, system.ErrInvalid)
		}
	}
}

func extractZip(r io.ReaderAt, size int64, dir string, strip int) error {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return err
	}
	for _, f := range zr.File {
		target, err := archivePath(dir, f.Name, strip)
		if err != nil {
			return err
		}
		if target == "" {
			continue
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
			continue
		}
		if !f.Mode().IsRegular() {
			return fmt.Errorf("archive member %q has an unsupported type: %w", f.Name, system.ErrInvalid)
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		err = writeMember(target, rc, f.Mode().Perm())
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func writeMember(target string, r io.Reader, mode fs.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode|0o400)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
