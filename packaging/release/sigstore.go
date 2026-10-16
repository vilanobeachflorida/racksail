package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SigstoreTUF is Sigstore's TUF repository, which publishes its trusted
// root: the keys of its certificate authority and transparency logs. The
// install script pins one by its SHA-256 checksum, the name the repository
// serves it under, and checks signatures against it when it installs
// through a mirror.
const SigstoreTUF = "https://tuf-repo-cdn.sigstore.dev"

// SigstoreRootFile is the version of the repository's own TUF root that
// check-pins starts from. Each newer root must be signed by enough keys of
// the root before it, so the trusted root's checksum is only believed when a
// chain of signatures leads to it from this file. check-pins -write moves
// the file on to the newest root it verified.
const SigstoreRootFile = "packaging/release/sigstore-root.json"

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
	// TUFRoot is the newest version of the repository's TUF root, as the
	// repository served it, and TUFVersion its version. Its keys signed the
	// metadata that lists the trusted root.
	TUFRoot    []byte
	TUFVersion int
}

// URL is where the repository serves the trusted root.
func (r TrustedRoot) URL(base string) string {
	return base + "/targets/" + r.SHA256 + ".trusted_root.json"
}

// Limits on what the repository serves; its own files are far smaller.
const (
	maxRootSize      = 512 << 10
	maxTimestampSize = 64 << 10
	maxMetadataSize  = 4 << 20
	// maxRootRotations bounds the new roots one check follows.
	maxRootRotations = 256
)

// CurrentTrustedRoot reads the trusted root Sigstore's TUF repository lists,
// checking every file on the way as the TUF specification's client does.
// Starting from tufRoot, a version of the repository's root that is trusted
// as it is, it follows each newer root, which must be signed by enough keys
// of the root before it and of its own. The newest root's keys then must
// have signed the timestamp, which names the snapshot, which names the
// targets, which list trusted_root.json with its checksum; none may have
// expired by now.
func CurrentTrustedRoot(ctx context.Context, client *http.Client, base string, tufRoot []byte, now time.Time) (TrustedRoot, error) {
	repo := &tufRepository{client: client, base: strings.TrimSuffix(base, "/"), now: now}
	root, rootData, err := repo.updateRoot(ctx, tufRoot)
	if err != nil {
		return TrustedRoot{}, err
	}
	if err := repo.unexpired(root.Expires, fmt.Sprintf("version %d of the TUF root", root.Version)); err != nil {
		return TrustedRoot{}, err
	}

	data, err := repo.get(ctx, "timestamp.json", maxTimestampSize)
	if err != nil {
		return TrustedRoot{}, err
	}
	var timestamp tufMeta
	if err := repo.open(data, root, "timestamp", &timestamp); err != nil {
		return TrustedRoot{}, err
	}
	var snapshot tufMeta
	if err := repo.follow(ctx, root, &timestamp, "snapshot", &snapshot); err != nil {
		return TrustedRoot{}, err
	}
	var targets tufMeta
	if err := repo.follow(ctx, root, &snapshot, "targets", &targets); err != nil {
		return TrustedRoot{}, err
	}

	target, ok := targets.Targets["trusted_root.json"]
	if !ok {
		return TrustedRoot{}, errors.New("the targets of Sigstore's repository don't list trusted_root.json")
	}
	sum := target.Hashes["sha256"]
	if !sha256Hex.MatchString(sum) || target.Length < 1 {
		return TrustedRoot{}, fmt.Errorf("the targets of Sigstore's repository list trusted_root.json without a SHA-256 checksum and length: %q, %d", sum, target.Length)
	}
	return TrustedRoot{SHA256: sum, Length: target.Length, TUFRoot: rootData, TUFVersion: root.Version}, nil
}

// tufRepository reads the files of a TUF repository.
type tufRepository struct {
	client *http.Client
	base   string
	now    time.Time
}

type tufSignature struct {
	KeyID string `json:"keyid"`
	Sig   string `json:"sig"`
}

// signedFile is a metadata file: what it signs, in the canonical form the
// signatures cover, and the signatures.
type signedFile struct {
	signatures []tufSignature
	canonical  []byte
}

type tufKey struct {
	KeyType string `json:"keytype"`
	Scheme  string `json:"scheme"`
	KeyVal  struct {
		Public string `json:"public"`
	} `json:"keyval"`
}

type tufRole struct {
	KeyIDs    []string `json:"keyids"`
	Threshold int      `json:"threshold"`
}

type tufRoot struct {
	Type               string             `json:"_type"`
	Version            int                `json:"version"`
	Expires            string             `json:"expires"`
	ConsistentSnapshot bool               `json:"consistent_snapshot"`
	Keys               map[string]tufKey  `json:"keys"`
	Roles              map[string]tufRole `json:"roles"`
}

// tufFile describes another file: a metadata file in the timestamp and the
// snapshot, a target in the targets.
type tufFile struct {
	Version int               `json:"version"`
	Length  int64             `json:"length"`
	Hashes  map[string]string `json:"hashes"`
}

// tufMeta is the timestamp, the snapshot or the targets.
type tufMeta struct {
	Type    string             `json:"_type"`
	Version int                `json:"version"`
	Expires string             `json:"expires"`
	Meta    map[string]tufFile `json:"meta"`
	Targets map[string]tufFile `json:"targets"`
}

var errNotFound = errors.New("not found")

func (r *tufRepository) get(ctx context.Context, name string, limit int64) ([]byte, error) {
	url := r.base + "/" + name
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound, http.StatusForbidden:
		// Repositories on object storage answer 403 for files they don't have.
		return nil, fmt.Errorf("%s: %s: %w", url, resp.Status, errNotFound)
	default:
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, fmt.Errorf("%s is larger than %d bytes", url, limit)
	}
	return data, nil
}

// updateRoot starts from a root that is trusted as it is and follows every
// newer root the repository has.
func (r *tufRepository) updateRoot(ctx context.Context, data []byte) (*tufRoot, []byte, error) {
	root := &tufRoot{}
	file, err := readSigned(data, root)
	if err != nil {
		return nil, nil, fmt.Errorf("the TUF root to start from: %w", err)
	}
	if root.Type != "root" {
		return nil, nil, fmt.Errorf("the TUF root to start from is a %q file", root.Type)
	}
	if err := file.signedBy(root.Keys, root.Roles["root"]); err != nil {
		return nil, nil, fmt.Errorf("version %d of the TUF root, which check-pins starts from, isn't signed by its own keys: %w", root.Version, err)
	}
	for range maxRootRotations {
		next := root.Version + 1
		newData, err := r.get(ctx, fmt.Sprintf("%d.root.json", next), maxRootSize)
		if errors.Is(err, errNotFound) {
			return root, data, nil
		}
		if err != nil {
			return nil, nil, err
		}
		newer := &tufRoot{}
		file, err := readSigned(newData, newer)
		if err != nil {
			return nil, nil, fmt.Errorf("version %d of the TUF root: %w", next, err)
		}
		if newer.Type != "root" {
			return nil, nil, fmt.Errorf("version %d of the TUF root is a %q file", next, newer.Type)
		}
		// The keys of the root before vouch for the new one, and the new
		// one's own keys accept it.
		if err := file.signedBy(root.Keys, root.Roles["root"]); err != nil {
			return nil, nil, fmt.Errorf("version %d of the TUF root isn't signed by the keys of version %d: %w", next, root.Version, err)
		}
		if err := file.signedBy(newer.Keys, newer.Roles["root"]); err != nil {
			return nil, nil, fmt.Errorf("version %d of the TUF root isn't signed by its own keys: %w", next, err)
		}
		if newer.Version != next {
			return nil, nil, fmt.Errorf("the repository serves version %d of the TUF root as version %d", newer.Version, next)
		}
		root, data = newer, newData
	}
	return nil, nil, fmt.Errorf("the repository has more than %d new versions of its TUF root", maxRootRotations)
}

// open checks a metadata file's type, its signatures by the keys root gives
// its role, and its expiry, and decodes what it signs into meta.
func (r *tufRepository) open(data []byte, root *tufRoot, role string, meta *tufMeta) error {
	file, err := readSigned(data, meta)
	if err != nil {
		return fmt.Errorf("the %s: %w", role, err)
	}
	if meta.Type != role {
		return fmt.Errorf("the %s is a %q file", role, meta.Type)
	}
	if err := file.signedBy(root.Keys, root.Roles[role]); err != nil {
		return fmt.Errorf("the %s isn't signed by the keys version %d of the TUF root names for it: %w", role, root.Version, err)
	}
	return r.unexpired(meta.Expires, "the "+role)
}

// follow reads the metadata file of role that from names, and checks it
// against what from says of it.
func (r *tufRepository) follow(ctx context.Context, root *tufRoot, from *tufMeta, role string, meta *tufMeta) error {
	name := role + ".json"
	want, ok := from.Meta[name]
	if !ok || want.Version < 1 {
		return fmt.Errorf("the %s names no version of the %s", from.Type, role)
	}
	if root.ConsistentSnapshot {
		name = fmt.Sprintf("%d.%s", want.Version, name)
	}
	limit := int64(maxMetadataSize)
	if want.Length > 0 && want.Length < limit {
		limit = want.Length
	}
	data, err := r.get(ctx, name, limit)
	if err != nil {
		return err
	}
	if err := want.matches(data); err != nil {
		return fmt.Errorf("the %s isn't the one the %s names: %w", role, from.Type, err)
	}
	if err := r.open(data, root, role, meta); err != nil {
		return err
	}
	if meta.Version != want.Version {
		return fmt.Errorf("the %s is version %d, but the %s names version %d", role, meta.Version, from.Type, want.Version)
	}
	return nil
}

func (r *tufRepository) unexpired(expires, what string) error {
	t, err := time.Parse(time.RFC3339, expires)
	if err != nil {
		return fmt.Errorf("%s expires at %q, which isn't a time", what, expires)
	}
	if !r.now.Before(t) {
		return fmt.Errorf("%s expired at %s", what, t.UTC().Format(time.RFC3339))
	}
	return nil
}

// matches checks a file against the length and checksums given for it.
func (f tufFile) matches(data []byte) error {
	if f.Length > 0 && int64(len(data)) != f.Length {
		return fmt.Errorf("it is %d bytes, not %d", len(data), f.Length)
	}
	for alg, want := range f.Hashes {
		var got []byte
		switch alg {
		case "sha256":
			sum := sha256.Sum256(data)
			got = sum[:]
		case "sha512":
			sum := sha512.Sum512(data)
			got = sum[:]
		default:
			continue
		}
		if hex.EncodeToString(got) != strings.ToLower(want) {
			return fmt.Errorf("its %s checksum isn't %s", alg, want)
		}
	}
	return nil
}

// readSigned reads a metadata file and decodes what it signs into v.
func readSigned(data []byte, v any) (*signedFile, error) {
	var env struct {
		Signatures []tufSignature  `json:"signatures"`
		Signed     json.RawMessage `json:"signed"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, err
	}
	if len(env.Signed) == 0 {
		return nil, errors.New("it signs nothing")
	}
	canonical, err := canonicalJSON(env.Signed)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(env.Signed, v); err != nil {
		return nil, err
	}
	return &signedFile{signatures: env.Signatures, canonical: canonical}, nil
}

// signedBy checks that at least the role's threshold of its keys made
// valid signatures. A key listed under several ids counts once.
func (f *signedFile) signedBy(keys map[string]tufKey, role tufRole) error {
	if role.Threshold < 1 {
		return fmt.Errorf("the role's threshold is %d", role.Threshold)
	}
	allowed := map[string]bool{}
	for _, id := range role.KeyIDs {
		allowed[id] = true
	}
	signers := map[string]bool{}
	for _, s := range f.signatures {
		key, ok := keys[s.KeyID]
		if !ok || !allowed[s.KeyID] {
			continue
		}
		verify, identity, err := key.verifier()
		if err != nil {
			continue
		}
		sig, err := hex.DecodeString(s.Sig)
		if err != nil || len(sig) == 0 || !verify(f.canonical, sig) {
			continue
		}
		signers[identity] = true
	}
	if len(signers) < role.Threshold {
		return fmt.Errorf("%d of its keys signed it, and %d must", len(signers), role.Threshold)
	}
	return nil
}

// verifier returns what checks a signature made with the key, and the key's
// identity.
func (k tufKey) verifier() (func(msg, sig []byte) bool, string, error) {
	switch {
	case (k.KeyType == "ecdsa" || k.KeyType == "ecdsa-sha2-nistp256") && k.Scheme == "ecdsa-sha2-nistp256":
		pub, err := parseP256(k.KeyVal.Public)
		if err != nil {
			return nil, "", err
		}
		raw, err := pub.Bytes()
		if err != nil {
			return nil, "", err
		}
		return func(msg, sig []byte) bool {
			digest := sha256.Sum256(msg)
			return ecdsa.VerifyASN1(pub, digest[:], sig)
		}, "ecdsa:" + string(raw), nil
	case k.KeyType == "ed25519" && k.Scheme == "ed25519":
		pub, err := hex.DecodeString(k.KeyVal.Public)
		if err != nil || len(pub) != ed25519.PublicKeySize {
			return nil, "", errors.New("isn't an Ed25519 public key")
		}
		return func(msg, sig []byte) bool { return ed25519.Verify(pub, msg, sig) }, "ed25519:" + string(pub), nil
	}
	return nil, "", fmt.Errorf("%s keys with the scheme %s aren't supported", k.KeyType, k.Scheme)
}

// parseP256 reads an ECDSA P-256 public key in PEM, or as the hex-encoded
// point Sigstore's first roots used.
func parseP256(public string) (*ecdsa.PublicKey, error) {
	if block, _ := pem.Decode([]byte(public)); block != nil {
		key, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, err
		}
		pub, ok := key.(*ecdsa.PublicKey)
		if !ok || pub.Curve != elliptic.P256() {
			return nil, errors.New("isn't an ECDSA P-256 public key")
		}
		return pub, nil
	}
	raw, err := hex.DecodeString(public)
	if err != nil {
		return nil, err
	}
	return ecdsa.ParseUncompressedPublicKey(elliptic.P256(), raw)
}

// canonicalJSON writes a JSON value in the canonical form TUF signs: object
// keys sorted, no whitespace, only quotes and backslashes escaped in
// strings, and integers as the only numbers.
func canonicalJSON(data []byte) ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return nil, err
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("there is more after the JSON value")
	}
	var b bytes.Buffer
	if err := writeCanonical(&b, v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func writeCanonical(b *bytes.Buffer, v any) error {
	switch v := v.(type) {
	case nil:
		b.WriteString("null")
	case bool:
		b.WriteString(strconv.FormatBool(v))
	case json.Number:
		n, err := strconv.ParseInt(v.String(), 10, 64)
		if err != nil {
			return fmt.Errorf("canonical JSON has only integers, not %s", v)
		}
		b.WriteString(strconv.FormatInt(n, 10))
	case string:
		writeCanonicalString(b, v)
	case []any:
		b.WriteByte('[')
		for i, item := range v {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := writeCanonical(b, item); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		b.WriteByte('{')
		for i, k := range keys {
			if i > 0 {
				b.WriteByte(',')
			}
			writeCanonicalString(b, k)
			b.WriteByte(':')
			if err := writeCanonical(b, v[k]); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	default:
		return fmt.Errorf("canonical JSON can't hold %T", v)
	}
	return nil
}

func writeCanonicalString(b *bytes.Buffer, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		if s[i] == '"' || s[i] == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	b.WriteByte('"')
}
