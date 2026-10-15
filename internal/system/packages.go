package system

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// Packages installs software: Debian packages from pinned apt sources and
// vendor archives verified against checksums.
type Packages interface {
	// Installed returns the installed packages among names. Packages that
	// aren't installed are left out.
	Installed(ctx context.Context, names ...string) ([]Package, error)
	// Candidates returns what Install would install for names, with versions
	// and sizes, including dependencies that aren't installed yet.
	Candidates(ctx context.Context, names ...string) ([]Package, error)
	UpdateIndex(ctx context.Context) error
	Install(ctx context.Context, pkgs ...PackageRequest) error
	Remove(ctx context.Context, purge bool, names ...string) error
	// Upgradable lists installed packages with newer versions available.
	Upgradable(ctx context.Context) ([]Package, error)
	// AddSource writes an apt source with its signing key and pin. Call
	// UpdateIndex afterwards.
	AddSource(ctx context.Context, src AptSource) error
	RemoveSource(ctx context.Context, name string) error
	// Sources lists the names of the apt sources Racksail added.
	Sources(ctx context.Context) ([]string, error)
	// InstallArchive downloads a vendor archive, verifies its checksum and
	// unpacks it into a new versioned directory. An existing directory is
	// left alone, since versioned directories never change.
	InstallArchive(ctx context.Context, a Archive) error
}

// Package is a Debian package version.
type Package struct {
	Name           string `json:"name"`
	Version        string `json:"version"`
	Arch           string `json:"arch,omitempty"`
	DownloadBytes  int64  `json:"downloadBytes,omitempty"`
	InstalledBytes int64  `json:"installedBytes,omitempty"`
	// Origin is the repository the version comes from.
	Origin string `json:"origin,omitempty"`
}

// PackageRequest asks for a package, optionally at an exact version.
type PackageRequest struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

// AptSource is a vendor repository, pinned so it can only provide the
// packages it is added for.
type AptSource struct {
	// Name identifies the source; files are named racksail-<name>.
	Name          string   `json:"name"`
	URIs          []string `json:"uris"`
	Suites        []string `json:"suites"`
	Components    []string `json:"components,omitempty"`
	Architectures []string `json:"architectures,omitempty"`
	// Key is the OpenPGP public key the repository is signed with, armored.
	Key []byte `json:"key"`
	// Packages limits the source to these package names (globs allowed).
	// Everything else from it gets a negative pin priority.
	Packages []string `json:"packages"`
}

// Archive is a vendor archive to unpack into a versioned directory.
type Archive struct {
	URL string `json:"url"`
	// SHA256 is the expected checksum in hex. It is required.
	SHA256 string `json:"sha256"`
	// Size is the expected size in bytes; larger downloads are refused.
	Size int64 `json:"size"`
	// Format is tar.gz, tar.xz or zip.
	Format string `json:"format"`
	// Dest is the directory to create, such as
	// /opt/racksail/runtimes/node/22.11.0.
	Dest            string `json:"dest"`
	StripComponents int    `json:"stripComponents,omitempty"`
}

var (
	sourceNamePattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,62}$`)
	aptWordPattern     = regexp.MustCompile(`^[A-Za-z0-9._/+-]+$`)
	packageGlobPattern = regexp.MustCompile(`^[a-z0-9.+*-]+$`)
	sha256Pattern      = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// Validate checks a source before anything is written.
func (s AptSource) Validate() error {
	if !sourceNamePattern.MatchString(s.Name) {
		return fmt.Errorf("apt source name %q: %w", s.Name, ErrInvalid)
	}
	if len(s.URIs) == 0 || len(s.Suites) == 0 || len(s.Packages) == 0 {
		return fmt.Errorf("apt source %s needs URIs, suites and the packages it provides: %w", s.Name, ErrInvalid)
	}
	for _, u := range s.URIs {
		parsed, err := url.Parse(u)
		if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
			return fmt.Errorf("apt source %s: URI %q must be https: %w", s.Name, u, ErrInvalid)
		}
	}
	for _, w := range append(append(append([]string{}, s.Suites...), s.Components...), s.Architectures...) {
		if !aptWordPattern.MatchString(w) {
			return fmt.Errorf("apt source %s: %q: %w", s.Name, w, ErrInvalid)
		}
	}
	for _, p := range s.Packages {
		if !packageGlobPattern.MatchString(p) {
			return fmt.Errorf("apt source %s: package %q: %w", s.Name, p, ErrInvalid)
		}
	}
	if !bytes.Contains(s.Key, []byte("-----BEGIN PGP PUBLIC KEY BLOCK-----")) {
		return fmt.Errorf("apt source %s needs an armored signing key: %w", s.Name, ErrInvalid)
	}
	return nil
}

// AptFiles are the files that make up an apt source.
type AptFiles struct {
	SourcesPath string
	Sources     []byte
	KeyPath     string
	Key         []byte
	PrefsPath   string
	Prefs       []byte
}

// Render returns the deb822 sources file, the keyring and the pin
// preferences for the source.
func (s AptSource) Render() (AptFiles, error) {
	if err := s.Validate(); err != nil {
		return AptFiles{}, err
	}
	stem := "racksail-" + s.Name
	f := AptFiles{
		SourcesPath: path.Join(AptSourceDir, stem+".sources"),
		KeyPath:     path.Join(AptKeyDir, stem+".asc"),
		Key:         s.Key,
		PrefsPath:   path.Join(AptPrefsDir, stem+".pref"),
	}
	var b bytes.Buffer
	fmt.Fprintf(&b, "%s\nTypes: deb\nURIs: %s\nSuites: %s\n", GeneratedHeader, strings.Join(s.URIs, " "), strings.Join(s.Suites, " "))
	if len(s.Components) > 0 {
		fmt.Fprintf(&b, "Components: %s\n", strings.Join(s.Components, " "))
	}
	if len(s.Architectures) > 0 {
		fmt.Fprintf(&b, "Architectures: %s\n", strings.Join(s.Architectures, " "))
	}
	fmt.Fprintf(&b, "Signed-By: %s\n", f.KeyPath)
	f.Sources = b.Bytes()

	host := ""
	if u, err := url.Parse(s.URIs[0]); err == nil {
		host = u.Hostname()
	}
	var p bytes.Buffer
	fmt.Fprintf(&p, "%s\n", GeneratedHeader)
	fmt.Fprintf(&p, "Package: *\nPin: origin %s\nPin-Priority: -1\n\n", host)
	fmt.Fprintf(&p, "Package: %s\nPin: origin %s\nPin-Priority: 600\n", strings.Join(s.Packages, " "), host)
	f.Prefs = p.Bytes()
	return f, nil
}

// Validate checks an archive request.
func (a Archive) Validate() error {
	u, err := url.Parse(a.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return fmt.Errorf("archive URL %q must be https: %w", a.URL, ErrInvalid)
	}
	if !sha256Pattern.MatchString(a.SHA256) {
		return fmt.Errorf("archive %s needs a lowercase hex SHA-256: %w", a.URL, ErrInvalid)
	}
	if a.Size <= 0 {
		return fmt.Errorf("archive %s needs its expected size: %w", a.URL, ErrInvalid)
	}
	switch a.Format {
	case "tar.gz", "tar.xz", "zip":
	default:
		return fmt.Errorf("archive format %q: %w", a.Format, ErrInvalid)
	}
	if !path.IsAbs(a.Dest) || path.Clean(a.Dest) != a.Dest {
		return fmt.Errorf("archive destination %q: %w", a.Dest, ErrInvalid)
	}
	if a.StripComponents < 0 || a.StripComponents > 8 {
		return fmt.Errorf("archive strip components %d: %w", a.StripComponents, ErrInvalid)
	}
	return nil
}
