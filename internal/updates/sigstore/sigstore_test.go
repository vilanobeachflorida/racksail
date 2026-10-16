package sigstore_test

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/updates/sigstore"
	"github.com/vilanobeachflorida/racksail/internal/updates/sigstore/sigstoretest"
)

var release = []byte("the racksail binary")

func setup(t *testing.T) (*sigstoretest.Sigstore, *sigstore.TrustedRoot) {
	t.Helper()
	s := sigstoretest.New()
	root, err := sigstore.ParseTrustedRoot(s.TrustedRoot())
	if err != nil {
		t.Fatal(err)
	}
	return s, root
}

func digest(b []byte) []byte {
	sum := sha256.Sum256(b)
	return sum[:]
}

func TestVerifyRekorV1(t *testing.T) {
	s, root := setup(t)
	id := sigstore.Identity{Subject: sigstoretest.Identity("v1.2.3"), Issuer: sigstoretest.Issuer}
	bundle := s.Sign(release, id.Subject, id.Issuer, sigstoretest.Options{})
	res, err := sigstore.Verify(root, bundle, digest(release), id)
	if err != nil {
		t.Fatal(err)
	}
	if res.SignedAt.IsZero() || res.LogIndex <= 1000 || res.Certificate == nil {
		t.Errorf("result = %+v", res)
	}
}

func TestVerifyRekorV2WithTimestamp(t *testing.T) {
	s, root := setup(t)
	id := sigstore.Identity{Subject: sigstoretest.Identity("v1.2.3"), Issuer: sigstoretest.Issuer}
	bundle := s.Sign(release, id.Subject, id.Issuer, sigstoretest.Options{RekorV2: true})
	if _, err := sigstore.Verify(root, bundle, digest(release), id); err != nil {
		t.Fatal(err)
	}
}

func TestRefusals(t *testing.T) {
	s, root := setup(t)
	id := sigstore.Identity{Subject: sigstoretest.Identity("v1.2.3"), Issuer: sigstoretest.Issuer}
	good := s.Sign(release, id.Subject, id.Issuer, sigstoretest.Options{})
	other, otherRoot := setup(t)
	edit := func(b []byte, f func(m map[string]any)) []byte {
		var m map[string]any
		json.Unmarshal(b, &m)
		f(m)
		out, _ := json.Marshal(m)
		return out
	}
	entry := func(m map[string]any) map[string]any {
		return m["verificationMaterial"].(map[string]any)["tlogEntries"].([]any)[0].(map[string]any)
	}
	for name, tc := range map[string]struct {
		root   *sigstore.TrustedRoot
		bundle []byte
		file   []byte
		id     sigstore.Identity
		want   string
	}{
		"another file":     {root, good, []byte("something else"), id, "another file"},
		"another identity": {root, good, release, sigstore.Identity{Subject: sigstoretest.Identity("v9.9.9"), Issuer: id.Issuer}, "not https://github.com"},
		"another issuer":   {root, good, release, sigstore.Identity{Subject: id.Subject, Issuer: "https://accounts.example.com"}, "vouched for by"},
		"another sigstore": {otherRoot, good, release, id, ""},
		"signed elsewhere": {root, other.Sign(release, id.Subject, id.Issuer, sigstoretest.Options{}), release, id, ""},
		"no sct":           {root, s.Sign(release, id.Subject, id.Issuer, sigstoretest.Options{NoSCT: true}), release, id, "certificate transparency"},
		"expired":          {root, s.Sign(release, id.Subject, id.Issuer, sigstoretest.Options{Expired: true}), release, id, "wasn't valid"},
		"forged promise": {root, edit(good, func(m map[string]any) {
			entry(m)["integratedTime"] = "1700000000"
		}), release, id, "promise"},
		"not logged": {root, edit(good, func(m map[string]any) {
			m["verificationMaterial"].(map[string]any)["tlogEntries"] = []any{}
		}), release, id, "never logged"},
		"bad proof": {root, edit(good, func(m map[string]any) {
			p := entry(m)["inclusionProof"].(map[string]any)
			p["hashes"] = p["hashes"].([]any)[1:]
		}), release, id, "proof"},
		"another body": {root, edit(good, func(m map[string]any) {
			body, _ := base64.StdEncoding.DecodeString(entry(m)["canonicalizedBody"].(string))
			entry(m)["canonicalizedBody"] = base64.StdEncoding.EncodeToString([]byte(strings.Replace(string(body), `"sha256"`, `"sha512"`, 1)))
		}), release, id, "another file"},
		"attestation": {root, edit(good, func(m map[string]any) {
			delete(m, "messageSignature")
			m["dsseEnvelope"] = map[string]any{"payload": "e30="}
		}), release, id, "attestation"},
	} {
		_, err := sigstore.Verify(tc.root, tc.bundle, digest(tc.file), tc.id)
		if err == nil {
			t.Errorf("%s: verified", name)
			continue
		}
		if tc.want != "" && !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: %v, want it to mention %q", name, err, tc.want)
		}
	}
	// Without a log's promise, a Rekor v2 entry needs a timestamp.
	v2 := s.Sign(release, id.Subject, id.Issuer, sigstoretest.Options{RekorV2: true})
	noTime := edit(v2, func(m map[string]any) {
		delete(m["verificationMaterial"].(map[string]any), "timestampVerificationData")
	})
	if _, err := sigstore.Verify(root, noTime, digest(release), id); err == nil || !strings.Contains(err.Error(), "vouches for when") {
		t.Errorf("a signature nothing dates is refused: %v", err)
	}
	otherTS := edit(v2, func(m map[string]any) {
		m["verificationMaterial"].(map[string]any)["timestampVerificationData"] = map[string]any{"rfc3161Timestamps": []any{
			map[string]string{"signedTimestamp": base64.StdEncoding.EncodeToString(other.Timestamp([]byte("sig"), s.TSA.NotBefore))},
		}}
	})
	if _, err := sigstore.Verify(root, otherTS, digest(release), id); err == nil || !strings.Contains(err.Error(), "timestamp") {
		t.Errorf("a timestamp from another authority is refused: %v", err)
	}
}

func TestParseTrustedRootRefusals(t *testing.T) {
	for name, data := range map[string]string{
		"not json":      "nope",
		"not a root":    `{"mediaType":"application/json"}`,
		"no authority":  `{"mediaType":"application/vnd.dev.sigstore.trustedroot+json;version=0.1"}`,
		"bad key bytes": `{"mediaType":"application/vnd.dev.sigstore.trustedroot+json;version=0.1","tlogs":[{"publicKey":{"rawBytes":"AAAA"},"logId":{"keyId":"AA=="}}]}`,
	} {
		if _, err := sigstore.ParseTrustedRoot([]byte(data)); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}
