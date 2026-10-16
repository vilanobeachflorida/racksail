package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sshStub stands in for ssh: it runs the remote command here.
const sshStub = `#!/bin/sh
for last; do :; done
exec sh -c "$last"
`

// installStubs puts stand-ins for programs first on the PATH.
func installStubs(t *testing.T, stubs map[string]string) {
	t.Helper()
	bin := t.TempDir()
	for name, script := range stubs {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// upgradeStubs stand in for ssh and for the server's tools, which keep
// their state in $STATE: apt-get marks the package upgraded, and systemctl
// reports the services down after the upgrade when the test marks them
// broken.
var upgradeStubs = map[string]string{
	"ssh":        sshStub,
	"cloud-init": "#!/bin/sh\nexit 0\n",
	"curl": `#!/bin/sh
while [ $# -gt 0 ]; do
	case $1 in
	--output) out=$2; shift ;;
	esac
	shift
done
echo package >"$STATE/$(basename "$out")"
`,
	"sudo": "#!/bin/sh\nexec env \"$@\"\n",
	"apt-get": `#!/bin/sh
echo "DEBIAN_FRONTEND=$DEBIAN_FRONTEND apt-get $*" >>"$STATE/apt.log"
touch "$STATE/upgraded"
echo "Setting up racksail"
`,
	"dpkg-query": "#!/bin/sh\necho 'racksail 1.3.0 install ok installed'\n",
	"systemctl": `#!/bin/sh
if [ -e "$STATE/upgraded" ] && [ -e "$STATE/broken" ]; then
	echo failed
	exit 3
fi
echo active
`,
}

func selfSigned(t *testing.T) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.IPv4(127, 0, 0, 1)},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// upgradeRig starts a panel that runs 1.2.4 until apt-get has run and 1.3.0
// after, with the stubs on the PATH. Markers in the state directory change
// what happens: "stuck" keeps the panel on 1.2.4, "new-certificate" makes it
// serve another certificate after the upgrade, and "broken" stops its
// services.
func upgradeRig(t *testing.T, markers ...string) (panel, state, logs string) {
	state, logs = t.TempDir(), t.TempDir()
	for _, m := range markers {
		if err := os.WriteFile(filepath.Join(state, m), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	installStubs(t, upgradeStubs)
	t.Setenv("STATE", state)
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(state, name))
		return err == nil
	}

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		version := "1.2.4"
		if has("upgraded") && !has("stuck") {
			version = "1.3.0"
		}
		fmt.Fprintf(w, `{"version":%q,"channel":"stable"}`, version)
	}))
	before, after := selfSigned(t), selfSigned(t)
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{before},
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			if has("upgraded") && has("new-certificate") {
				return &tls.Config{Certificates: []tls.Certificate{after}}, nil
			}
			return nil, nil
		},
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv.URL, state, logs
}

func TestUpgrade(t *testing.T) {
	server := SSH{Key: "unused", Port: 22, User: "admin"}
	const url = "http://10.0.2.2:8080/racksail.deb"

	panel, state, logs := upgradeRig(t)
	if err := upgrade(context.Background(), server, panel, url, "1.3.0", 10*time.Second, logs); err != nil {
		t.Fatal(err)
	}
	apt, _ := os.ReadFile(filepath.Join(state, "apt.log"))
	if string(apt) != "DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-install-recommends -o DPkg::Lock::Timeout=600 /tmp/racksail.deb\n" {
		t.Errorf("apt-get ran as %q", apt)
	}
	if _, err := os.Stat(filepath.Join(state, "racksail.deb")); err != nil {
		t.Error("the package wasn't downloaded from the harness")
	}
	out, _ := os.ReadFile(filepath.Join(logs, "apt-upgrade.txt"))
	if !strings.Contains(string(out), "Setting up racksail") || !strings.Contains(string(out), "racksail 1.3.0 install ok installed") {
		t.Errorf("apt's output isn't in the logs: %q", out)
	}

	failures := []struct {
		markers []string
		version string
		timeout time.Duration
		want    string
	}{
		{[]string{"broken"}, "1.3.0", 10 * time.Second, "after the upgrade: not all of Racksail's services are enabled and running"},
		{[]string{"new-certificate"}, "1.3.0", 10 * time.Second, "serves another certificate"},
		{[]string{"stuck"}, "1.3.0", 3 * time.Second, "didn't come back on 1.3.0 (last answer: version 1.2.4)"},
		{nil, "1.2.4", 10 * time.Second, "isn't older than 1.2.4"},
	}
	for _, tc := range failures {
		panel, state, logs := upgradeRig(t, tc.markers...)
		err := upgrade(context.Background(), server, panel, url, tc.version, tc.timeout, logs)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: the upgrade must fail with %q: %v", tc.markers, tc.want, err)
		}
		if tc.version == "1.2.4" {
			if _, err := os.Stat(filepath.Join(state, "apt.log")); err == nil {
				t.Error("nothing may be installed when the earlier package isn't older")
			}
		}
	}
}
