package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSigstoreRootPin(t *testing.T) {
	script, err := os.ReadFile("../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	pin, err := SigstoreRootPin(script)
	if err != nil || len(pin) != 64 {
		t.Fatalf("pin = %q, %v", pin, err)
	}
	other := strings.Repeat("ab", 32)
	updated, err := UpdateSigstoreRootPin(script, other)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := SigstoreRootPin(updated); got != other {
		t.Errorf("updated pin = %s", got)
	}
	if !bytes.Equal(bytes.Replace(updated, []byte(other), []byte(pin), 1), script) {
		t.Error("updating the pin changed more than the checksum")
	}
	if _, err := UpdateSigstoreRootPin(script, "not a checksum"); err == nil {
		t.Error("a pin that isn't a checksum must be refused")
	}
	if _, err := SigstoreRootPin([]byte("#!/bin/sh\n")); err == nil {
		t.Error("a script without the pin must fail")
	}
	if _, err := UpdateSigstoreRootPin([]byte("#!/bin/sh\n"), other); err == nil {
		t.Error("a script without the pin can't be updated")
	}
}

func TestCanonicalJSON(t *testing.T) {
	cases := map[string]string{
		`{"b": 1, "a": "x\"y\\z", "c": [true, false, null]}`:          `{"a":"x\"y\\z","b":1,"c":[true,false,null]}`,
		`{"d": {"z": "é", "a": "<>& \n"}, "e": -0, "f": 12345678901}`: "{\"d\":{\"a\":\"<>& \n\",\"z\":\"é\"},\"e\":0,\"f\":12345678901}",
		` [ ] `: `[]`,
		`{}`:    `{}`,
	}
	for in, want := range cases {
		got, err := canonicalJSON([]byte(in))
		if err != nil || string(got) != want {
			t.Errorf("canonicalJSON(%s) = %s, %v; want %s", in, got, err, want)
		}
	}
	for _, bad := range []string{`{"a": 1.5}`, `{"a": 1e3}`, `{"a": 1} {}`, `{`} {
		if got, err := canonicalJSON([]byte(bad)); err == nil {
			t.Errorf("canonicalJSON(%s) = %s; want an error", bad, got)
		}
	}
}

// serveTUF serves a repository's files under /tuf/, answering missing ones
// with missing, and cosign's checksums under /cosign/.
func serveTUF(t *testing.T, files map[string][]byte, missing int, cosignSums map[string]string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /tuf/{name}", func(w http.ResponseWriter, r *http.Request) {
		data, ok := files[r.PathValue("name")]
		if !ok {
			w.WriteHeader(missing)
			return
		}
		w.Write(data)
	})
	mux.HandleFunc("GET /cosign/{version}/cosign_checksums.txt", func(w http.ResponseWriter, r *http.Request) {
		for name, sum := range cosignSums {
			fmt.Fprintf(w, "%s  %s\n", sum, name)
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func readDir(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, e := range entries {
		data, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		files[e.Name()] = data
	}
	return files
}

// Sigstore's own repository as it was on 15 October 2026: every version of
// its TUF root from the first, and the timestamp, snapshot and targets of
// that day. Following it checks the canonical form and the signatures
// against what Sigstore signed, in every format its roots have used.
func TestSigstoreRepository(t *testing.T) {
	files := readDir(t, "testdata/sigstore-tuf")
	srv := serveTUF(t, files, http.StatusNotFound, nil)
	ctx := context.Background()
	day := time.Date(2026, 10, 15, 12, 0, 0, 0, time.UTC)

	got, err := CurrentTrustedRoot(ctx, srv.Client(), srv.URL+"/tuf", files["1.root.json"], day)
	if err != nil {
		t.Fatal(err)
	}
	if got.SHA256 != "6494e21ea73fa7ee769f85f57d5a3e6a08725eae1e38c755fc3517c9e6bc0b66" || got.Length != 6787 {
		t.Errorf("trusted root = %s, %d bytes", got.SHA256, got.Length)
	}
	if got.TUFVersion != 15 || !bytes.Equal(got.TUFRoot, files["15.root.json"]) {
		t.Errorf("the newest TUF root is version %d", got.TUFVersion)
	}
	if want := srv.URL + "/tuf/targets/" + got.SHA256 + ".trusted_root.json"; got.URL(srv.URL+"/tuf") != want {
		t.Errorf("URL = %s, want %s", got.URL(srv.URL+"/tuf"), want)
	}
	if again, err := CurrentTrustedRoot(ctx, srv.Client(), srv.URL+"/tuf", files["14.root.json"], day); err != nil || again.SHA256 != got.SHA256 {
		t.Errorf("starting from version 14: %+v, %v", again, err)
	}

	// A week later the timestamp has expired, and so has everything it says.
	if _, err := CurrentTrustedRoot(ctx, srv.Client(), srv.URL+"/tuf", files["1.root.json"], day.AddDate(0, 0, 8)); err == nil || !strings.Contains(err.Error(), "the timestamp expired at 2026-10-22T13:29:19Z") {
		t.Errorf("an expired timestamp must fail: %v", err)
	}

	// A root with a key of someone else's in place of one of Sigstore's
	// isn't signed by the root before it.
	forged := maps.Clone(files)
	var root map[string]any
	json.Unmarshal(files["15.root.json"], &root)
	signed := root["signed"].(map[string]any)
	keys := signed["keys"].(map[string]any)
	for id, key := range keys {
		key.(map[string]any)["keyval"] = map[string]any{"public": newTUFKey(t).public()["keyval"].(map[string]any)["public"]}
		keys[id] = key
		break
	}
	forged["15.root.json"], _ = json.Marshal(root)
	srv = serveTUF(t, forged, http.StatusNotFound, nil)
	if _, err := CurrentTrustedRoot(ctx, srv.Client(), srv.URL+"/tuf", files["13.root.json"], day); err == nil || !strings.Contains(err.Error(), "version 15 of the TUF root isn't signed by the keys of version 14") {
		t.Errorf("a forged root must fail: %v", err)
	}
}

// The TUF root check-pins starts from is a root that its own keys signed,
// and while it is one of the versions in testdata, it is that very file.
func TestSigstoreRootFile(t *testing.T) {
	data, err := os.ReadFile("sigstore-root.json")
	if err != nil {
		t.Fatal(err)
	}
	var root tufRoot
	file, err := readSigned(data, &root)
	if err != nil {
		t.Fatal(err)
	}
	if root.Type != "root" || root.Version < 1 {
		t.Fatalf("sigstore-root.json is a %q file, version %d", root.Type, root.Version)
	}
	if err := file.signedBy(root.Keys, root.Roles["root"]); err != nil {
		t.Errorf("sigstore-root.json isn't signed by its own keys: %v", err)
	}
	if known, err := os.ReadFile(fmt.Sprintf("testdata/sigstore-tuf/%d.root.json", root.Version)); err == nil && !bytes.Equal(known, data) {
		t.Errorf("sigstore-root.json differs from version %d of Sigstore's TUF root", root.Version)
	}
}

// tufTestKey is a key of a test repository.
type tufTestKey struct {
	id  string
	key *ecdsa.PrivateKey
}

func newTUFKey(t *testing.T) tufTestKey {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(der)
	return tufTestKey{id: hex.EncodeToString(sum[:]), key: key}
}

func (k tufTestKey) public() map[string]any {
	der, _ := x509.MarshalPKIXPublicKey(&k.key.PublicKey)
	return map[string]any{
		"keytype": "ecdsa",
		"scheme":  "ecdsa-sha2-nistp256",
		"keyval":  map[string]any{"public": string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))},
	}
}

// testTUF builds a repository laid out like Sigstore's: three versions of
// the root, the last two with one root key rotated, where two root keys
// must sign; a targets key; and an online key for the timestamp and the
// snapshot. Its targets list trusted_root.json with the checksum trusted.
type testTUF struct {
	t       *testing.T
	keys    map[string]tufTestKey
	files   map[string][]byte
	trusted string
	expires string
}

func newTestTUF(t *testing.T, trusted string) *testTUF {
	r := &testTUF{t: t, keys: map[string]tufTestKey{}, files: map[string][]byte{}, trusted: trusted,
		expires: time.Now().AddDate(1, 0, 0).UTC().Format(time.RFC3339)}
	for _, name := range []string{"r1", "r2", "r3", "r4", "targets", "online", "stranger"} {
		r.keys[name] = newTUFKey(t)
	}
	r.files["1.root.json"] = r.sign(r.root(1, "r1", "r2", "r3"), "r1", "r2", "r3")
	r.files["2.root.json"] = r.sign(r.root(2, "r2", "r3", "r4"), "r1", "r2", "r4")
	r.files["3.root.json"] = r.sign(r.root(3, "r2", "r3", "r4"), "r3", "r4")
	r.publish(r.targets(7, trusted))
	return r
}

// root is what a root of the version with the given root keys signs.
func (r *testTUF) root(version int, rootKeys ...string) map[string]any {
	keys := map[string]any{}
	var ids []string
	for _, name := range rootKeys {
		keys[r.keys[name].id] = r.keys[name].public()
		ids = append(ids, r.keys[name].id)
	}
	for _, name := range []string{"targets", "online"} {
		keys[r.keys[name].id] = r.keys[name].public()
	}
	role := func(threshold int, ids ...string) map[string]any {
		return map[string]any{"keyids": ids, "threshold": threshold}
	}
	return map[string]any{
		"_type": "root", "spec_version": "1.0", "version": version, "expires": r.expires, "consistent_snapshot": true,
		"keys": keys,
		"roles": map[string]any{
			"root":      role(2, ids...),
			"targets":   role(1, r.keys["targets"].id),
			"snapshot":  role(1, r.keys["online"].id),
			"timestamp": role(1, r.keys["online"].id),
		},
	}
}

func (r *testTUF) targets(version int, trusted string) map[string]any {
	targets := map[string]any{"rekor.pub": map[string]any{"length": 178, "hashes": map[string]any{"sha256": strings.Repeat("0", 64)}}}
	if trusted != "" {
		targets["trusted_root.json"] = map[string]any{"length": 6787, "hashes": map[string]any{"sha256": trusted}}
	}
	return map[string]any{"_type": "targets", "spec_version": "1.0", "version": version, "expires": r.expires, "targets": targets}
}

// meta describes a file the way the timestamp and the snapshot do.
func meta(version int, data []byte) map[string]any {
	sum := sha256.Sum256(data)
	return map[string]any{"version": version, "length": len(data), "hashes": map[string]any{"sha256": hex.EncodeToString(sum[:])}}
}

// publish signs the targets and writes a snapshot and a timestamp that name
// them.
func (r *testTUF) publish(targets map[string]any) {
	version := targets["version"].(int)
	name := fmt.Sprintf("%d.targets.json", version)
	r.files[name] = r.sign(targets, "targets")
	r.publishSnapshot(map[string]any{"_type": "snapshot", "spec_version": "1.0", "version": 5, "expires": r.expires,
		"meta": map[string]any{"targets.json": meta(version, r.files[name])}})
}

func (r *testTUF) publishSnapshot(snapshot map[string]any) {
	version := snapshot["version"].(int)
	name := fmt.Sprintf("%d.snapshot.json", version)
	r.files[name] = r.sign(snapshot, "online")
	r.timestamp(version, r.files[name])
}

// timestamp writes a timestamp that names the snapshot of the version,
// whose file is data.
func (r *testTUF) timestamp(version int, data []byte) {
	r.files["timestamp.json"] = r.sign(map[string]any{"_type": "timestamp", "spec_version": "1.0", "version": 10, "expires": r.expires,
		"meta": map[string]any{"snapshot.json": meta(version, data)}}, "online")
}

// sign makes a metadata file of signed, with signatures by the named keys.
func (r *testTUF) sign(signed map[string]any, signers ...string) []byte {
	keys := make([]tufTestKey, len(signers))
	for i, name := range signers {
		keys[i] = r.keys[name]
	}
	return signTUF(r.t, signed, keys...)
}

func signTUF(t *testing.T, signed map[string]any, keys ...tufTestKey) []byte {
	t.Helper()
	raw, err := json.Marshal(signed)
	if err != nil {
		t.Fatal(err)
	}
	canonical, err := canonicalJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(canonical)
	sigs := []map[string]any{}
	for _, k := range keys {
		sig, err := ecdsa.SignASN1(rand.Reader, k.key, digest[:])
		if err != nil {
			t.Fatal(err)
		}
		sigs = append(sigs, map[string]any{"keyid": k.id, "sig": hex.EncodeToString(sig)})
	}
	data, err := json.MarshalIndent(map[string]any{"signatures": sigs, "signed": signed}, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestCurrentTrustedRoot(t *testing.T) {
	trusted := strings.Repeat("6", 64)
	past := time.Now().AddDate(0, 0, -1).UTC().Format(time.RFC3339)
	cases := []struct {
		name    string
		change  func(r *testTUF)
		missing int
		want    string
	}{
		{"three roots", nil, http.StatusNotFound, ""},
		// Object storage answers 403 for files it doesn't have.
		{"roots on object storage", nil, http.StatusForbidden, ""},
		{"a new root too few old keys signed", func(r *testTUF) {
			r.files["2.root.json"] = r.sign(r.root(2, "r2", "r3", "r4"), "r1", "r4")
		}, http.StatusNotFound, "version 2 of the TUF root isn't signed by the keys of version 1: 1 of its keys signed it, and 2 must"},
		{"a new root its own keys didn't sign", func(r *testTUF) {
			r.files["2.root.json"] = r.sign(r.root(2, "r2", "r3", "r4"), "r1", "r2")
		}, http.StatusNotFound, "version 2 of the TUF root isn't signed by its own keys"},
		{"a root that says it is another version", func(r *testTUF) {
			r.files["2.root.json"] = r.sign(r.root(3, "r2", "r3", "r4"), "r1", "r2", "r4")
		}, http.StatusNotFound, "serves version 3 of the TUF root as version 2"},
		{"a file that isn't a root", func(r *testTUF) {
			root := r.root(2, "r2", "r3", "r4")
			root["_type"] = "targets"
			r.files["2.root.json"] = r.sign(root, "r1", "r2", "r4")
		}, http.StatusNotFound, `version 2 of the TUF root is a "targets" file`},
		{"an expired root", func(r *testTUF) {
			root := r.root(3, "r2", "r3", "r4")
			root["expires"] = past
			r.files["3.root.json"] = r.sign(root, "r3", "r4")
		}, http.StatusNotFound, "version 3 of the TUF root expired"},
		{"a timestamp someone else signed", func(r *testTUF) {
			var ts struct {
				Signed map[string]any `json:"signed"`
			}
			json.Unmarshal(r.files["timestamp.json"], &ts)
			r.files["timestamp.json"] = r.sign(ts.Signed, "stranger")
		}, http.StatusNotFound, "the timestamp isn't signed by the keys version 3 of the TUF root names for it: 0 of its keys signed it, and 1 must"},
		{"an expired timestamp", func(r *testTUF) {
			r.expires = past
			r.publishSnapshot(map[string]any{"_type": "snapshot", "spec_version": "1.0", "version": 5, "expires": r.expires,
				"meta": map[string]any{"targets.json": meta(7, r.files["7.targets.json"])}})
		}, http.StatusNotFound, "the timestamp expired"},
		{"a snapshot that changed after the timestamp", func(r *testTUF) {
			r.files["5.snapshot.json"] = r.sign(map[string]any{"_type": "snapshot", "spec_version": "1.0", "version": 5, "expires": r.expires,
				"meta": map[string]any{"targets.json": map[string]any{"version": 8}}}, "online")
		}, http.StatusNotFound, "the snapshot isn't the one the timestamp names"},
		{"a snapshot of another version than the timestamp names", func(r *testTUF) {
			r.files["5.snapshot.json"] = r.sign(map[string]any{"_type": "snapshot", "spec_version": "1.0", "version": 4, "expires": r.expires,
				"meta": map[string]any{"targets.json": meta(7, r.files["7.targets.json"])}}, "online")
			r.timestamp(5, r.files["5.snapshot.json"])
		}, http.StatusNotFound, "the snapshot is version 4, but the timestamp names version 5"},
		{"targets of another version than the snapshot names", func(r *testTUF) {
			r.publishSnapshot(map[string]any{"_type": "snapshot", "spec_version": "1.0", "version": 5, "expires": r.expires,
				"meta": map[string]any{"targets.json": meta(6, r.files["7.targets.json"])}})
			r.files["6.targets.json"] = r.files["7.targets.json"]
		}, http.StatusNotFound, "the targets is version 7, but the snapshot names version 6"},
		{"targets changed after they were signed", func(r *testTUF) {
			r.files["7.targets.json"] = bytes.Replace(r.files["7.targets.json"], []byte(trusted), []byte(strings.Repeat("9", 64)), 1)
			r.publishSnapshot(map[string]any{"_type": "snapshot", "spec_version": "1.0", "version": 5, "expires": r.expires,
				"meta": map[string]any{"targets.json": meta(7, r.files["7.targets.json"])}})
		}, http.StatusNotFound, "the targets isn't signed by the keys version 3 of the TUF root names for it"},
		{"targets without the trusted root", func(r *testTUF) {
			r.publish(r.targets(7, ""))
		}, http.StatusNotFound, "don't list trusted_root.json"},
		{"one key under two ids", func(r *testTUF) {
			alias := tufTestKey{id: "alias", key: r.keys["targets"].key}
			root := r.root(4, "r2", "r3", "r4")
			root["keys"].(map[string]any)["alias"] = alias.public()
			root["roles"].(map[string]any)["targets"] = map[string]any{"keyids": []string{r.keys["targets"].id, "alias"}, "threshold": 2}
			r.files["4.root.json"] = r.sign(root, "r2", "r3")
			targets := r.targets(7, trusted)
			r.files["7.targets.json"] = signTUF(t, targets, r.keys["targets"], alias)
			r.publishSnapshot(map[string]any{"_type": "snapshot", "spec_version": "1.0", "version": 5, "expires": r.expires,
				"meta": map[string]any{"targets.json": meta(7, r.files["7.targets.json"])}})
		}, http.StatusNotFound, "1 of its keys signed it, and 2 must"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := newTestTUF(t, trusted)
			if tc.change != nil {
				tc.change(r)
			}
			srv := serveTUF(t, r.files, tc.missing, nil)
			got, err := CurrentTrustedRoot(context.Background(), srv.Client(), srv.URL+"/tuf", r.files["1.root.json"], time.Now())
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("CurrentTrustedRoot = %+v, %v; want an error containing %q", got, err, tc.want)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.SHA256 != trusted || got.Length != 6787 || got.TUFVersion != 3 || !bytes.Equal(got.TUFRoot, r.files["3.root.json"]) {
				t.Errorf("trusted root = %s, %d bytes, from version %d", got.SHA256, got.Length, got.TUFVersion)
			}
		})
	}

	t.Run("a root to start from that its keys didn't sign", func(t *testing.T) {
		r := newTestTUF(t, trusted)
		srv := serveTUF(t, r.files, http.StatusNotFound, nil)
		start := r.sign(r.root(1, "r1", "r2", "r3"), "r1")
		if _, err := CurrentTrustedRoot(context.Background(), srv.Client(), srv.URL+"/tuf", start, time.Now()); err == nil || !strings.Contains(err.Error(), "check-pins starts from, isn't signed by its own keys") {
			t.Errorf("an unsigned root to start from must fail: %v", err)
		}
	})
	t.Run("a repository that fails", func(t *testing.T) {
		r := newTestTUF(t, trusted)
		srv := serveTUF(t, r.files, http.StatusInternalServerError, nil)
		if _, err := CurrentTrustedRoot(context.Background(), srv.Client(), srv.URL+"/tuf", r.files["1.root.json"], time.Now()); err == nil || !strings.Contains(err.Error(), "500") {
			t.Errorf("a server error must fail rather than end the roots: %v", err)
		}
	})
	t.Run("without consistent snapshots", func(t *testing.T) {
		r := newTestTUF(t, trusted)
		root := r.root(1, "r1", "r2", "r3")
		root["consistent_snapshot"] = false
		files := map[string][]byte{"1.root.json": r.sign(root, "r1", "r2"), "timestamp.json": r.files["timestamp.json"],
			"snapshot.json": r.files["5.snapshot.json"], "targets.json": r.files["7.targets.json"]}
		srv := serveTUF(t, files, http.StatusNotFound, nil)
		if got, err := CurrentTrustedRoot(context.Background(), srv.Client(), srv.URL+"/tuf", files["1.root.json"], time.Now()); err != nil || got.SHA256 != trusted {
			t.Errorf("trusted root = %+v, %v", got, err)
		}
	})
}

func TestCheckPins(t *testing.T) {
	original, err := os.ReadFile("../install.sh")
	if err != nil {
		t.Fatal(err)
	}
	_, sums, err := CosignPins(original)
	if err != nil {
		t.Fatal(err)
	}
	pin, _ := SigstoreRootPin(original)
	dir := t.TempDir()
	pins := pinFiles{Script: filepath.Join(dir, "install.sh"), TUFRoot: filepath.Join(dir, "sigstore-root.json")}
	if err := os.WriteFile(pins.Script, original, 0o755); err != nil {
		t.Fatal(err)
	}
	cosignSums := map[string]string{"cosign-linux-amd64": sums["amd64"], "cosign-linux-arm64": sums["arm64"]}
	repo := newTestTUF(t, pin)
	if err := os.WriteFile(pins.TUFRoot, repo.files["2.root.json"], 0o644); err != nil {
		t.Fatal(err)
	}
	check := func(write bool) (string, error) {
		srv := serveTUF(t, repo.files, http.StatusNotFound, cosignSums)
		var out bytes.Buffer
		err := checkPins(context.Background(), srv.Client(), pins, srv.URL+"/cosign", srv.URL+"/tuf", write, &out)
		return out.String(), err
	}

	if out, err := check(false); err != nil || !strings.Contains(out, "pins Sigstore's current trusted root, which version 3 of Sigstore's TUF root vouches for") {
		t.Fatalf("check-pins = %q, %v", out, err)
	}

	// Sigstore publishes a new trusted root, with a new version of the TUF
	// root.
	newRoot := strings.Repeat("7", 64)
	repo.files["4.root.json"] = repo.sign(repo.root(4, "r2", "r3", "r4"), "r2", "r4")
	repo.publish(repo.targets(8, newRoot))
	_, err = check(false)
	if err == nil || !strings.Contains(err.Error(), "Sigstore's current one is "+newRoot) || !strings.Contains(err.Error(), "check-pins -write") {
		t.Errorf("a new trusted root must be reported: %v", err)
	}
	if out, err := check(true); err != nil || !strings.Contains(out, "now holds version 4 of Sigstore's TUF root") {
		t.Fatalf("check-pins -write = %q, %v", out, err)
	}
	if _, err := check(false); err != nil {
		t.Errorf("after -write: %v", err)
	}
	data, _ := os.ReadFile(pins.Script)
	if got, _ := SigstoreRootPin(data); got != newRoot {
		t.Errorf("-write pinned %s", got)
	}
	if !bytes.Equal(bytes.Replace(data, []byte(newRoot), []byte(pin), 1), original) {
		t.Error("-write changed more than the pin")
	}
	if root, _ := os.ReadFile(pins.TUFRoot); !bytes.Equal(root, repo.files["4.root.json"]) {
		t.Error("-write didn't keep the newest TUF root to start from next time")
	}

	// A repository whose signatures don't lead to its trusted root from the
	// TUF root check-pins starts from isn't believed.
	impostor := newTestTUF(t, strings.Repeat("8", 64))
	repo = impostor
	if _, err := check(false); err == nil || !strings.Contains(err.Error(), "the timestamp isn't signed by the keys version 4 of the TUF root names for it") {
		t.Errorf("a repository with other keys must fail: %v", err)
	}
}
