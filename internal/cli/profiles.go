package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

// Profile says how to reach one panel: its address, how to trust its
// certificate and the API token to sign in with.
type Profile struct {
	Server string `json:"server"`
	CAFile string `json:"caFile,omitempty"`
	Token  string `json:"token,omitempty"`
	// User and TokenName are who the token signed in as, and its name,
	// when it was saved, for listing profiles without calling the panel.
	User      string    `json:"user,omitempty"`
	TokenName string    `json:"tokenName,omitempty"`
	SavedAt   time.Time `json:"savedAt,omitzero"`
}

// Profiles are the panels the command knows, kept in one file only its
// user can read, because it holds API tokens.
type Profiles struct {
	Current  string              `json:"current,omitempty"`
	Profiles map[string]*Profile `json:"profiles"`

	path string
}

// ConfigEnv names a different profiles file than the default one.
const ConfigEnv = "RACKSAIL_CONFIG"

// ProfilesPath is where the profiles are kept: RACKSAIL_CONFIG, or
// racksail/cli.json in the user's configuration directory.
func ProfilesPath() (string, error) {
	if p := os.Getenv(ConfigEnv); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("finding where to keep profiles: %w; set %s", err, ConfigEnv)
	}
	return filepath.Join(dir, "racksail", "cli.json"), nil
}

// LoadProfiles reads the profiles file; a missing one holds no profiles.
// A file other users can read is refused, as it may hold tokens they
// shouldn't have seen.
func LoadProfiles() (*Profiles, error) {
	path, err := ProfilesPath()
	if err != nil {
		return nil, err
	}
	p := &Profiles{Profiles: map[string]*Profile{}, path: path}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if runtime.GOOS != "windows" {
		fi, err := f.Stat()
		if err != nil {
			return nil, err
		}
		if fi.Mode().Perm()&0o077 != 0 {
			return nil, fmt.Errorf("%s holds API tokens but other users can read it; run chmod 600 %s", path, path)
		}
	}
	if err := json.NewDecoder(f).Decode(p); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	if p.Profiles == nil {
		p.Profiles = map[string]*Profile{}
	}
	return p, nil
}

// Save writes the profiles, replacing the file in one step so a crash
// never leaves half of it.
func (p *Profiles) Save() error {
	if err := os.MkdirAll(filepath.Dir(p.path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(p.path), ".cli-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), p.path)
}

// Path is where the profiles are kept.
func (p *Profiles) Path() string { return p.path }

// Names lists the profiles by name.
func (p *Profiles) Names() []string {
	names := make([]string, 0, len(p.Profiles))
	for name := range p.Profiles {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Get returns a profile by name: the current one when name is empty.
func (p *Profiles) Get(name string) (string, *Profile, bool) {
	if name == "" {
		name = p.Current
	}
	prof, ok := p.Profiles[name]
	return name, prof, ok
}

// ProfileName makes a profile's name from its server's address: the
// host name, with the port when it isn't the usual one.
func ProfileName(server string) string {
	u, err := url.Parse(server)
	if err != nil || u.Host == "" {
		return "default"
	}
	name := u.Hostname()
	if port := u.Port(); port != "" && port != "443" && port != "80" {
		name += "-" + port
	}
	return strings.ReplaceAll(name, ":", "-")
}

// NormalizeServer checks a panel's address and gives it a scheme when it
// has none: https, except for loopback addresses, where racksail dev and
// tunnels to a panel listen without TLS.
func NormalizeServer(server string) (string, error) {
	server = strings.TrimSpace(server)
	if server == "" {
		return "", errors.New("give the panel's address with --server, such as https://panel.example.com")
	}
	if !strings.Contains(server, "://") {
		scheme := "https://"
		host := server
		if h, _, ok := strings.Cut(server, "/"); ok {
			host = h
		}
		if isLoopback(host) {
			scheme = "http://"
		}
		server = scheme + server
	}
	u, err := url.Parse(server)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("%s isn't a panel address; use one such as https://panel.example.com", server)
	}
	if u.User != nil {
		return "", errors.New("put the API token in a profile or RACKSAIL_TOKEN, not in the panel's address")
	}
	u.Path = strings.TrimSuffix(u.Path, "/")
	u.RawQuery, u.Fragment = "", ""
	return u.String(), nil
}

func isLoopback(host string) bool {
	h := host
	if strings.HasPrefix(h, "[") {
		h = strings.TrimPrefix(strings.SplitN(h, "]", 2)[0], "[")
	} else if i := strings.LastIndex(h, ":"); i >= 0 && strings.Count(h, ":") == 1 {
		h = h[:i]
	}
	return h == "localhost" || strings.HasPrefix(h, "127.") || h == "::1"
}
