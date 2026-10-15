package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

var (
	packageNamePattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9.+-]+$`)
	packageVersionPattern = regexp.MustCompile(`^[A-Za-z0-9.+~:-]+$`)
)

func checkPackageNames(names []string) error {
	if len(names) == 0 {
		return fmt.Errorf("no packages given")
	}
	for _, n := range names {
		if !packageNamePattern.MatchString(n) {
			return fmt.Errorf("package name %q", n)
		}
	}
	return nil
}

// InstallPackagesInput lists packages to install.
type InstallPackagesInput struct {
	Packages []system.PackageRequest `json:"packages" doc:"Packages, optionally at exact versions"`
}

// Validate implements Input.
func (in InstallPackagesInput) Validate() error {
	names := make([]string, len(in.Packages))
	for i, p := range in.Packages {
		names[i] = p.Name
		if p.Version != "" && !packageVersionPattern.MatchString(p.Version) {
			return fmt.Errorf("package version %q", p.Version)
		}
	}
	return checkPackageNames(names)
}

func packageEffects(pkgs []system.Package) []system.Effect {
	var out []system.Effect
	for _, p := range pkgs {
		out = append(out, system.Effect{
			Type: system.EffectPackage, Action: "install", Target: p.Name, Detail: p.Version,
			Source: p.Origin, DownloadBytes: p.DownloadBytes, DiskBytes: p.InstalledBytes,
		})
	}
	return out
}

// InstallPackages installs Debian packages that aren't installed yet, from
// Debian or from a pinned vendor source. In check mode it reports every
// package that would be installed, dependencies included, with sizes.
var InstallPackages = Register(Def[InstallPackagesInput, Result]{
	Name:    "InstallPackages",
	Summary: "Install packages",
	Effects: func(in InstallPackagesInput) []system.Effect {
		var out []system.Effect
		for _, p := range in.Packages {
			out = append(out, system.Effect{Type: system.EffectPackage, Action: "install", Target: p.Name, Detail: p.Version})
		}
		return out
	},
	Run: func(ctx context.Context, env *Env, in InstallPackagesInput) (Result, error) {
		pk := env.System.Packages
		names := make([]string, len(in.Packages))
		for i, p := range in.Packages {
			names[i] = p.Name
		}
		installed, err := pk.Installed(ctx, names...)
		if err != nil {
			return Result{}, err
		}
		have := map[string]string{}
		for _, p := range installed {
			have[p.Name] = p.Version
		}
		var missing []system.PackageRequest
		var missingNames []string
		for _, p := range in.Packages {
			v, ok := have[p.Name]
			if !ok || (p.Version != "" && v != p.Version) {
				missing = append(missing, p)
				missingNames = append(missingNames, p.Name)
			}
		}
		if len(missing) == 0 {
			return Unchanged(), nil
		}
		candidates, err := pk.Candidates(ctx, missingNames...)
		if err != nil {
			return Result{}, err
		}
		effects := packageEffects(candidates)
		if env.Check {
			return Changed(effects...), nil
		}
		return Changed(effects...), pk.Install(ctx, missing...)
	},
})

// RemovePackagesInput lists packages to remove.
type RemovePackagesInput struct {
	Names []string `json:"names" doc:"Package names"`
	Purge bool     `json:"purge,omitempty" doc:"Also remove configuration files"`
}

// Validate implements Input.
func (in RemovePackagesInput) Validate() error { return checkPackageNames(in.Names) }

// RemovePackages removes installed packages.
var RemovePackages = Register(Def[RemovePackagesInput, Result]{
	Name:    "RemovePackages",
	Summary: "Remove packages",
	Effects: func(in RemovePackagesInput) []system.Effect {
		var out []system.Effect
		for _, n := range in.Names {
			out = append(out, system.Effect{Type: system.EffectPackage, Action: "remove", Target: n})
		}
		return out
	},
	Run: func(ctx context.Context, env *Env, in RemovePackagesInput) (Result, error) {
		installed, err := env.System.Packages.Installed(ctx, in.Names...)
		if err != nil {
			return Result{}, err
		}
		if len(installed) == 0 {
			return Unchanged(), nil
		}
		var names []string
		var effects []system.Effect
		for _, p := range installed {
			names = append(names, p.Name)
			effects = append(effects, system.Effect{Type: system.EffectPackage, Action: "remove", Target: p.Name, Detail: p.Version, DiskBytes: -p.InstalledBytes})
		}
		if env.Check {
			return Changed(effects...), nil
		}
		return Changed(effects...), env.System.Packages.Remove(ctx, in.Purge, names...)
	},
})

var sourceNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,62}$`)

// SourceInput names a vendor repository registered with RegisterAptSource.
type SourceInput struct {
	Name string `json:"name" doc:"Registered source name"`
}

// Validate implements Input.
func (in SourceInput) Validate() error {
	if !sourceNamePattern.MatchString(in.Name) {
		return fmt.Errorf("source name %q", in.Name)
	}
	return nil
}

func registeredSource(name string) (system.AptSource, bool) {
	policy.RLock()
	defer policy.RUnlock()
	src, ok := policy.aptSources[name]
	return src, ok
}

// EnsureAptSource enables a vendor repository the binary knows about and
// refreshes the package index when it changed.
var EnsureAptSource = Register(Def[SourceInput, Result]{
	Name:        "EnsureAptSource",
	Summary:     "Enable a vendor package repository",
	Description: "Enables one of the vendor repositories registered in the binary, pinned so it can only provide the packages it is meant for.",
	Effects: func(in SourceInput) []system.Effect {
		return []system.Effect{{Type: system.EffectPackage, Action: "add-source", Target: in.Name}}
	},
	Run: func(ctx context.Context, env *Env, in SourceInput) (Result, error) {
		src, ok := registeredSource(in.Name)
		if !ok {
			return Result{}, ErrNotAllowed.New("op", "EnsureAptSource", "target", in.Name)
		}
		files, err := src.Render()
		if err != nil {
			return Result{}, err
		}
		current, err := env.System.Files.ReadFile(ctx, files.SourcesPath, 1<<20)
		if err == nil && bytes.Equal(current, files.Sources) {
			if key, err := env.System.Files.ReadFile(ctx, files.KeyPath, 1<<20); err == nil && bytes.Equal(key, files.Key) {
				return Unchanged(), nil
			}
		}
		effect := system.Effect{Type: system.EffectPackage, Action: "add-source", Target: in.Name, Source: strings.Join(src.URIs, " ")}
		if env.Check {
			return Changed(effect), nil
		}
		if err := env.System.Packages.AddSource(ctx, src); err != nil {
			return Result{}, err
		}
		return Changed(effect), env.System.Packages.UpdateIndex(ctx)
	},
})

// RemoveAptSource disables a vendor repository.
var RemoveAptSource = Register(Def[SourceInput, Result]{
	Name:    "RemoveAptSource",
	Summary: "Disable a vendor package repository",
	Effects: func(in SourceInput) []system.Effect {
		return []system.Effect{{Type: system.EffectPackage, Action: "remove-source", Target: in.Name}}
	},
	Run: func(ctx context.Context, env *Env, in SourceInput) (Result, error) {
		names, err := env.System.Packages.Sources(ctx)
		if err != nil {
			return Result{}, err
		}
		found := false
		for _, n := range names {
			found = found || n == in.Name
		}
		if !found {
			return Unchanged(), nil
		}
		effect := system.Effect{Type: system.EffectPackage, Action: "remove-source", Target: in.Name}
		if env.Check {
			return Changed(effect), nil
		}
		return Changed(effect), env.System.Packages.RemoveSource(ctx, in.Name)
	},
})

// Empty is the input of operations that take none.
type Empty struct{}

// Validate implements Input.
func (Empty) Validate() error { return nil }

// UpdatePackageIndex refreshes the package lists.
var UpdatePackageIndex = Register(Def[Empty, Result]{
	Name:    "UpdatePackageIndex",
	Summary: "Refresh the package lists",
	Effects: func(Empty) []system.Effect {
		return []system.Effect{{Type: system.EffectPackage, Action: "update-index", Target: "apt"}}
	},
	Run: func(ctx context.Context, env *Env, _ Empty) (Result, error) {
		effect := system.Effect{Type: system.EffectPackage, Action: "update-index", Target: "apt"}
		if env.Check {
			return Changed(effect), nil
		}
		return Changed(effect), env.System.Packages.UpdateIndex(ctx)
	},
})

// QueryPackagesInput lists packages to look up.
type QueryPackagesInput struct {
	Names []string `json:"names" doc:"Package names"`
}

// Validate implements Input.
func (in QueryPackagesInput) Validate() error { return checkPackageNames(in.Names) }

// PackageQuery reports installed packages and what installing the rest would
// add.
type PackageQuery struct {
	Installed  []system.Package `json:"installed"`
	Candidates []system.Package `json:"candidates"`
}

// QueryPackages reports which packages are installed and what the others
// would bring in.
var QueryPackages = Register(Def[QueryPackagesInput, PackageQuery]{
	Name:     "QueryPackages",
	Summary:  "Look up packages",
	ReadOnly: true,
	Run: func(ctx context.Context, env *Env, in QueryPackagesInput) (PackageQuery, error) {
		q := PackageQuery{Installed: []system.Package{}, Candidates: []system.Package{}}
		installed, err := env.System.Packages.Installed(ctx, in.Names...)
		if err != nil {
			return q, err
		}
		q.Installed = append(q.Installed, installed...)
		have := map[string]bool{}
		for _, p := range installed {
			have[p.Name] = true
		}
		var rest []string
		for _, n := range in.Names {
			if !have[n] {
				rest = append(rest, n)
			}
		}
		if len(rest) > 0 {
			candidates, err := env.System.Packages.Candidates(ctx, rest...)
			if err != nil && !errors.Is(err, system.ErrNotFound) {
				return q, err
			}
			q.Candidates = append(q.Candidates, candidates...)
		}
		return q, nil
	},
})

// PackageList lists packages.
type PackageList struct {
	Packages []system.Package `json:"packages"`
}

// UpgradablePackages lists installed packages with newer versions.
var UpgradablePackages = Register(Def[Empty, PackageList]{
	Name:     "UpgradablePackages",
	Summary:  "List packages with updates",
	ReadOnly: true,
	Run: func(ctx context.Context, env *Env, _ Empty) (PackageList, error) {
		pkgs, err := env.System.Packages.Upgradable(ctx)
		if pkgs == nil {
			pkgs = []system.Package{}
		}
		return PackageList{Packages: pkgs}, err
	},
})

// InstallArchiveInput is a vendor archive to unpack.
type InstallArchiveInput struct {
	Archive system.Archive `json:"archive"`
}

// Validate implements Input.
func (in InstallArchiveInput) Validate() error {
	if err := in.Archive.Validate(); err != nil {
		return err
	}
	if !under(in.Archive.Dest, []string{system.RuntimesDir, system.ModulesDir}) || in.Archive.Dest == system.RuntimesDir || in.Archive.Dest == system.ModulesDir {
		return fmt.Errorf("archives unpack into a versioned directory under %s or %s", system.RuntimesDir, system.ModulesDir)
	}
	return nil
}

// InstallArchive downloads a vendor archive, checks its checksum and unpacks
// it into a new versioned directory. Existing directories are never touched.
var InstallArchive = Register(Def[InstallArchiveInput, Result]{
	Name:    "InstallArchive",
	Summary: "Install a vendor archive",
	Effects: func(in InstallArchiveInput) []system.Effect {
		return archiveEffects(in.Archive)
	},
	Run: func(ctx context.Context, env *Env, in InstallArchiveInput) (Result, error) {
		if _, err := env.System.Files.Stat(ctx, in.Archive.Dest); err == nil {
			return Unchanged(), nil
		}
		effects := archiveEffects(in.Archive)
		if env.Check {
			return Changed(effects...), nil
		}
		return Changed(effects...), env.System.Packages.InstallArchive(ctx, in.Archive)
	},
})

func archiveEffects(a system.Archive) []system.Effect {
	return []system.Effect{
		{Type: system.EffectDownload, Action: "download", Target: a.Dest, Source: a.URL, DownloadBytes: a.Size},
		{Type: system.EffectFile, Action: "unpack", Target: a.Dest, DiskBytes: a.Size * 3},
	}
}
