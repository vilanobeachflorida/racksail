package linux

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

type network struct{ h *host }

type ipLink struct {
	IfIndex   int      `json:"ifindex"`
	IfName    string   `json:"ifname"`
	Flags     []string `json:"flags"`
	MTU       int      `json:"mtu"`
	OperState string   `json:"operstate"`
	LinkType  string   `json:"link_type"`
	Address   string   `json:"address"`
	Master    string   `json:"master"`
	LinkInfo  *struct {
		InfoKind string `json:"info_kind"`
	} `json:"linkinfo"`
	AddrInfo []struct {
		Family    string `json:"family"`
		Local     string `json:"local"`
		PrefixLen int    `json:"prefixlen"`
		Scope     string `json:"scope"`
		Dynamic   bool   `json:"dynamic"`
	} `json:"addr_info"`
}

// parseIPAddr converts `ip -json -details address show` output.
func parseIPAddr(out []byte) ([]system.Interface, error) {
	var links []ipLink
	if err := json.Unmarshal(out, &links); err != nil {
		return nil, fmt.Errorf("reading ip output: %w", err)
	}
	ifaces := make([]system.Interface, 0, len(links))
	for _, l := range links {
		ifc := system.Interface{
			Name: l.IfName, Index: l.IfIndex, MTU: l.MTU, Master: l.Master,
			Up:      contains(l.Flags, "UP"),
			Carrier: contains(l.Flags, "LOWER_UP"),
			Type:    "other",
		}
		if l.LinkType == "ether" {
			ifc.MAC = l.Address
			ifc.Type = "ether"
		}
		if l.LinkType == "loopback" {
			ifc.Type = "loopback"
		}
		if l.LinkInfo != nil {
			switch l.LinkInfo.InfoKind {
			case "bond", "vlan", "bridge", "wireguard":
				ifc.Type = l.LinkInfo.InfoKind
			case "veth", "tun", "macvlan", "ipvlan", "vxlan":
				ifc.Type = "other"
			}
		}
		for _, a := range l.AddrInfo {
			if a.Local == "" {
				continue
			}
			ifc.Addresses = append(ifc.Addresses, system.Address{CIDR: a.Local + "/" + strconv.Itoa(a.PrefixLen), Scope: a.Scope, Dynamic: a.Dynamic})
		}
		ifaces = append(ifaces, ifc)
	}
	return ifaces, nil
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func (n *network) Interfaces(ctx context.Context) ([]system.Interface, error) {
	out, err := n.h.exec(ctx, progIP, "-json", "-details", "address", "show")
	if err != nil {
		return nil, fmt.Errorf("listing interfaces: %w", err)
	}
	ifaces, err := parseIPAddr(out)
	if err != nil {
		return nil, err
	}
	for i := range ifaces {
		if data, err := os.ReadFile(n.h.path(path.Join("/sys/class/net", ifaces[i].Name, "speed"))); err == nil {
			if speed, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && speed > 0 {
				ifaces[i].SpeedMbps = speed
			}
		}
	}
	return ifaces, nil
}

// parseIPRoute converts `ip -json route show table all` output, leaving out
// the kernel's local and broadcast routes.
func parseIPRoute(out []byte, family int) ([]system.Route, error) {
	var raw []struct {
		Dst      string `json:"dst"`
		Gateway  string `json:"gateway"`
		Dev      string `json:"dev"`
		Metric   int    `json:"metric"`
		Table    any    `json:"table"`
		Type     string `json:"type"`
		Protocol string `json:"protocol"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("reading ip output: %w", err)
	}
	var routes []system.Route
	for _, r := range raw {
		table := 254
		switch t := r.Table.(type) {
		case string:
			switch t {
			case "local":
				continue
			case "main":
			default:
				if n, err := strconv.Atoi(t); err == nil {
					table = n
				}
			}
		case float64:
			table = int(t)
		}
		if r.Type == "local" || r.Type == "broadcast" || r.Type == "multicast" {
			continue
		}
		dst := r.Dst
		if dst == "default" {
			dst = "0.0.0.0/0"
			if family == 6 {
				dst = "::/0"
			}
		} else if !strings.Contains(dst, "/") {
			if family == 6 {
				dst += "/128"
			} else {
				dst += "/32"
			}
		}
		route := system.Route{Destination: dst, Gateway: r.Gateway, Interface: r.Dev, Metric: r.Metric}
		if table != 254 {
			route.Table = table
		}
		routes = append(routes, route)
	}
	return routes, nil
}

func (n *network) Routes(ctx context.Context) ([]system.Route, error) {
	var all []system.Route
	for _, family := range []int{4, 6} {
		out, err := n.h.exec(ctx, progIP, "-"+strconv.Itoa(family), "-json", "route", "show", "table", "all")
		if err != nil {
			return nil, fmt.Errorf("listing routes: %w", err)
		}
		routes, err := parseIPRoute(out, family)
		if err != nil {
			return nil, err
		}
		all = append(all, routes...)
	}
	return all, nil
}

var networkdFilePattern = regexp.MustCompile(`^50-racksail[A-Za-z0-9._-]*\.(network|netdev|link)$`)

func (n *network) ApplyConfig(ctx context.Context, files map[string][]byte) error {
	for name := range files {
		if !networkdFilePattern.MatchString(name) {
			return fmt.Errorf("networkd file name %q must start with 50-racksail: %w", name, system.ErrInvalid)
		}
	}
	f := &filesAdapter{n.h}
	current, err := n.Config(ctx)
	if err != nil {
		return err
	}
	for name := range current {
		if _, keep := files[name]; !keep {
			if err := f.remove(ctx, path.Join(system.NetworkdDir, name)); err != nil {
				return err
			}
		}
	}
	for name, data := range files {
		if err := f.write(ctx, path.Join(system.NetworkdDir, name), data); err != nil {
			return err
		}
	}
	_, err = n.h.exec(ctx, progNetworkctl, "reload")
	return wrap("reloading systemd-networkd", err)
}

func (n *network) Config(ctx context.Context) (map[string][]byte, error) {
	entries, err := os.ReadDir(n.h.path(system.NetworkdDir))
	if errors.Is(err, fs.ErrNotExist) {
		return map[string][]byte{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	for _, e := range entries {
		if !networkdFilePattern.MatchString(e.Name()) {
			continue
		}
		data, err := os.ReadFile(n.h.path(path.Join(system.NetworkdDir, e.Name())))
		if err != nil {
			return nil, err
		}
		out[e.Name()] = data
	}
	return out, nil
}

func (n *network) Hostname(ctx context.Context) (string, error) {
	data, err := os.ReadFile(n.h.path("/etc/hostname"))
	if err != nil {
		return "", mapErr("reading", "/etc/hostname", err)
	}
	return strings.TrimSpace(string(data)), nil
}

var hostnamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)

func (n *network) SetHostname(ctx context.Context, name string) error {
	if !hostnamePattern.MatchString(name) {
		return fmt.Errorf("hostname %q: %w", name, system.ErrInvalid)
	}
	_, err := n.h.exec(ctx, progHostnamectl, "set-hostname", "--", name)
	return wrap("setting the hostname", err)
}

// filesAdapter is a small wrapper for adapters that write root-owned
// configuration files.
type filesAdapter struct{ h *host }

func (f *filesAdapter) write(ctx context.Context, p string, data []byte) error {
	if err := os.MkdirAll(f.h.path(path.Dir(p)), 0o755); err != nil {
		return mapErr("write", p, err)
	}
	return (&files{f.h}).WriteFile(ctx, p, data, 0o644, system.Owner{})
}

func (f *filesAdapter) remove(ctx context.Context, p string) error {
	err := (&files{f.h}).Remove(ctx, p)
	if errors.Is(err, system.ErrNotFound) {
		return nil
	}
	return err
}
