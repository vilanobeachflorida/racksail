package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
)

// SigstoreTUF is Sigstore's TUF repository, which publishes its trusted
// root: the keys of its certificate authority and transparency logs. The
// install script pins one by its SHA-256 checksum, the name the repository
// serves it under, and checks signatures against it when it installs
// through a mirror.
const SigstoreTUF = "https://tuf-repo-cdn.sigstore.dev"

var pinRoot = regexp.MustCompile(`(?m)^SIGSTORE_ROOT_SHA256=([0-9a-f]{64})$`)

// SigstoreRootPin reads the checksum of the trusted root the install script
// pins.
func SigstoreRootPin(script []byte) (string, error) {
	m := pinRoot.FindSubmatch(script)
	if m == nil {
		return "", errors.New("the install script doesn't set SIGSTORE_ROOT_SHA256")
	}
	return string(m[1]), nil
}

// UpdateSigstoreRootPin writes the checksum of another trusted root into
// the install script.
func UpdateSigstoreRootPin(script []byte, sum string) ([]byte, error) {
	if !sha256Hex.MatchString(sum) {
		return nil, fmt.Errorf("%q isn't a SHA-256 checksum", sum)
	}
	if !pinRoot.Match(script) {
		return nil, errors.New("the install script has no SIGSTORE_ROOT_SHA256 line")
	}
	return pinRoot.ReplaceAll(script, []byte("SIGSTORE_ROOT_SHA256="+sum)), nil
}

// TrustedRoot is the trusted root Sigstore's TUF repository currently lists.
type TrustedRoot struct {
	SHA256 string
	Length int64
}

// URL is where the repository serves the trusted root.
func (r TrustedRoot) URL(base string) string {
	return base + "/targets/" + r.SHA256 + ".trusted_root.json"
}

// tufMeta is the part of a TUF metadata file that names the versions of
// other metadata files and the targets.
type tufMeta struct {
	Signed struct {
		Meta    map[string]struct{ Version int } `json:"meta"`
		Targets map[string]struct {
			Length int64             `json:"length"`
			Hashes map[string]string `json:"hashes"`
		} `json:"targets"`
	} `json:"signed"`
}

// CurrentTrustedRoot follows the repository's metadata from timestamp.json
// through the snapshot to the targets, which list the trusted root with its
// checksum. The metadata's signatures aren't checked: like cosign's
// published checksums, it is read over HTTPS to compare with a pin, and
// cosign checks the signatures whenever it uses the repository.
func CurrentTrustedRoot(ctx context.Context, client *http.Client, base string) (TrustedRoot, error) {
	var timestamp, snapshot, targets tufMeta
	if err := getJSON(ctx, client, base+"/timestamp.json", &timestamp); err != nil {
		return TrustedRoot{}, err
	}
	v := timestamp.Signed.Meta["snapshot.json"].Version
	if v < 1 {
		return TrustedRoot{}, errors.New("the timestamp of Sigstore's repository names no snapshot")
	}
	if err := getJSON(ctx, client, fmt.Sprintf("%s/%d.snapshot.json", base, v), &snapshot); err != nil {
		return TrustedRoot{}, err
	}
	v = snapshot.Signed.Meta["targets.json"].Version
	if v < 1 {
		return TrustedRoot{}, errors.New("the snapshot of Sigstore's repository names no targets")
	}
	if err := getJSON(ctx, client, fmt.Sprintf("%s/%d.targets.json", base, v), &targets); err != nil {
		return TrustedRoot{}, err
	}
	target, ok := targets.Signed.Targets["trusted_root.json"]
	if !ok {
		return TrustedRoot{}, errors.New("the targets of Sigstore's repository don't list trusted_root.json")
	}
	sum := target.Hashes["sha256"]
	if !sha256Hex.MatchString(sum) || target.Length < 1 {
		return TrustedRoot{}, fmt.Errorf("the targets of Sigstore's repository list trusted_root.json without a SHA-256 checksum and length: %q, %d", sum, target.Length)
	}
	return TrustedRoot{SHA256: sum, Length: target.Length}, nil
}

func getJSON(ctx context.Context, client *http.Client, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s: %w", url, err)
	}
	return nil
}
