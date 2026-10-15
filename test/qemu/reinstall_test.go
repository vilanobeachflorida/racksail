package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// reinstallStubs stand in for ssh and for the server's tools, which keep
// their state in $STATE: apt-get removes and installs the package, and
// systemctl reports the services down while it is removed. Markers change
// what happens: "stays-down" keeps the services down when the package is
// installed again, and "keeps-running" keeps them up when it's removed.
var reinstallStubs = map[string]string{
	"ssh":        sshStub,
	"cloud-init": "#!/bin/sh\nexit 0\n",
	"curl":       upgradeStubs["curl"],
	"sudo":       upgradeStubs["sudo"],
	"apt-get": `#!/bin/sh
echo "DEBIAN_FRONTEND=$DEBIAN_FRONTEND apt-get $*" >>"$STATE/apt.log"
case " $* " in
*" remove "*)
	touch "$STATE/removed"
	echo "Removing racksail"
	;;
*)
	rm -f "$STATE/removed"
	echo "Setting up racksail"
	;;
esac
`,
	"dpkg-query": `#!/bin/sh
if [ -e "$STATE/removed" ]; then
	echo 'racksail 1.3.0 deinstall ok config-files'
else
	echo 'racksail 1.3.0 install ok installed'
fi
`,
	"systemctl": `#!/bin/sh
status=0
shift
for unit; do
	if { [ -e "$STATE/removed" ] && [ ! -e "$STATE/keeps-running" ]; } || { [ ! -e "$STATE/removed" ] && [ -e "$STATE/stays-down" ] && [ -e "$STATE/apt.log" ]; }; then
		echo inactive
		status=3
	else
		echo active
	fi
done
exit $status
`,
}

// reinstallRig starts a panel that doesn't answer while the package is
// removed, with the stubs on the PATH. With "new-certificate" among the
// markers, it serves another certificate once the package is installed
// again.
func reinstallRig(t *testing.T, markers ...string) (panel, state, logs string) {
	state, logs = t.TempDir(), t.TempDir()
	for _, m := range markers {
		if err := os.WriteFile(filepath.Join(state, m), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	installStubs(t, reinstallStubs)
	t.Setenv("STATE", state)
	has := func(name string) bool {
		_, err := os.Stat(filepath.Join(state, name))
		return err == nil
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if has("removed") || has("stays-down") && has("apt.log") {
			http.Error(w, "the panel isn't running", http.StatusServiceUnavailable)
			return
		}
		fmt.Fprint(w, `{"status":"ok"}`)
	}))
	before, after := selfSigned(t), selfSigned(t)
	srv.TLS = &tls.Config{
		Certificates: []tls.Certificate{before},
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			if has("apt.log") && !has("removed") && has("new-certificate") {
				return &tls.Config{Certificates: []tls.Certificate{after}}, nil
			}
			return nil, nil
		},
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv.URL, state, logs
}

func TestReinstall(t *testing.T) {
	server := SSH{Key: "unused", Port: 22, User: "admin"}
	const url = "http://10.0.2.2:8080/racksail.deb"

	panel, state, logs := reinstallRig(t)
	if err := reinstall(context.Background(), server, panel, url, 10*time.Second, logs); err != nil {
		t.Fatal(err)
	}
	apt, _ := os.ReadFile(filepath.Join(state, "apt.log"))
	want := "DEBIAN_FRONTEND=noninteractive apt-get remove --yes -o DPkg::Lock::Timeout=600 racksail\n" +
		"DEBIAN_FRONTEND=noninteractive apt-get install --yes --no-install-recommends -o DPkg::Lock::Timeout=600 /tmp/racksail.deb\n"
	if string(apt) != want {
		t.Errorf("apt-get ran as\n%s\nwant\n%s", apt, want)
	}
	removed, _ := os.ReadFile(filepath.Join(logs, "apt-remove.txt"))
	if !strings.Contains(string(removed), "Removing racksail") || !strings.Contains(string(removed), "deinstall ok config-files") {
		t.Errorf("apt's output from the removal isn't in the logs: %q", removed)
	}
	if out, _ := os.ReadFile(filepath.Join(logs, "apt-reinstall.txt")); !strings.Contains(string(out), "install ok installed") {
		t.Errorf("apt's output from installing again isn't in the logs: %q", out)
	}

	failures := []struct {
		markers []string
		timeout time.Duration
		want    string
	}{
		{[]string{"keeps-running"}, 10 * time.Second, "racksail-agent.service, racksail-panel.service and racksail-edge.socket still run after the package was removed"},
		{[]string{"stays-down"}, 3 * time.Second, "after installing the package again: the panel didn't become healthy"},
		{[]string{"new-certificate"}, 10 * time.Second, "serves another certificate"},
	}
	for _, tc := range failures {
		panel, _, logs := reinstallRig(t, tc.markers...)
		err := reinstall(context.Background(), server, panel, url, tc.timeout, logs)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: reinstalling must fail with %q: %v", tc.markers, tc.want, err)
		}
	}
}

func TestCheckStopped(t *testing.T) {
	installStubs(t, map[string]string{
		"ssh":       sshStub,
		"systemctl": "#!/bin/sh\necho inactive\necho active\necho inactive\nexit 3\n",
	})
	err := checkStopped(context.Background(), SSH{})
	if err == nil || !strings.Contains(err.Error(), "racksail-panel.service still runs after") {
		t.Errorf("checkStopped = %v", err)
	}
	installStubs(t, map[string]string{"ssh": sshStub, "systemctl": "#!/bin/sh\necho 'Failed to connect to bus' >&2\nexit 1\n"})
	if err := checkStopped(context.Background(), SSH{}); err == nil || !strings.Contains(err.Error(), "Failed to connect to bus") {
		t.Errorf("an answer that isn't one state per unit must fail: %v", err)
	}
}

// When the panel never answered in the boot's time, SSH gets time of its
// own, so the server's diagnostics are still collected.
func TestWaitForSSH(t *testing.T) {
	installStubs(t, map[string]string{"ssh": "#!/bin/sh\nexit 0\n"})
	spent, cancel := context.WithCancel(context.Background())
	cancel()
	if err := waitForSSH(context.Background(), spent, SSH{}, true); err != nil {
		t.Errorf("after the panel failed: %v", err)
	}
	if err := waitForSSH(context.Background(), spent, SSH{}, false); err == nil {
		t.Error("with the panel up, SSH only has the boot's time")
	}
}
