package sigstore

import (
	"bytes"
	"crypto/sha256"
	"testing"

	"github.com/vilanobeachflorida/racksail/internal/updates/sigstore/sigstoretest"
)

var fuzzRelease = []byte("the racksail binary")

// fuzzSigstore signs the seeds of the fuzz targets.
func fuzzSigstore(f *testing.F) (*sigstoretest.Sigstore, *TrustedRoot, Identity) {
	s := sigstoretest.New()
	root, err := ParseTrustedRoot(s.TrustedRoot())
	if err != nil {
		f.Fatal(err)
	}
	return s, root, Identity{Subject: sigstoretest.Identity("v1.2.3"), Issuer: sigstoretest.Issuer}
}

// Bundles come from mirrors and upstream projects. Whatever one holds,
// reading it never panics.
func FuzzParseBundle(f *testing.F) {
	s, _, id := fuzzSigstore(f)
	f.Add(s.Sign(fuzzRelease, id.Subject, id.Issuer, sigstoretest.Options{}))
	f.Add(s.Sign(fuzzRelease, id.Subject, id.Issuer, sigstoretest.Options{RekorV2: true}))
	f.Add([]byte(`{"mediaType":"application/vnd.dev.sigstore.bundle.v0.3+json","messageSignature":{"signature":""}}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		b, err := ParseBundle(data)
		if err != nil {
			return
		}
		if b.Certificate == nil || len(b.Signature) == 0 {
			t.Fatalf("a bundle without a certificate or signature: %+v", b)
		}
	})
}

// Trusted roots are fetched from Sigstore and mirrors, and checked against
// the pin only after they parse.
func FuzzParseTrustedRoot(f *testing.F) {
	s, _, _ := fuzzSigstore(f)
	f.Add(s.TrustedRoot())
	f.Add([]byte(`{"mediaType":"application/vnd.dev.sigstore.trustedroot+json;version=0.1","tlogs":[{"publicKey":{"rawBytes":"AA=="}}]}`))
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = ParseTrustedRoot(data)
	})
}

// Whatever a bundle holds, its certificate chain, timestamps, log entries,
// inclusion proofs and checkpoints are checked without panicking, and a
// bundle only verifies when it signs the file for the identity asked for.
func FuzzVerify(f *testing.F) {
	s, root, id := fuzzSigstore(f)
	f.Add(s.Sign(fuzzRelease, id.Subject, id.Issuer, sigstoretest.Options{}))
	f.Add(s.Sign(fuzzRelease, id.Subject, id.Issuer, sigstoretest.Options{RekorV2: true}))
	f.Add(s.Sign([]byte("something else"), id.Subject, id.Issuer, sigstoretest.Options{}))
	sum := sha256.Sum256(fuzzRelease)
	f.Fuzz(func(t *testing.T, bundle []byte) {
		res, err := Verify(root, bundle, sum[:], id)
		if err != nil {
			return
		}
		if res.Certificate == nil || res.SignedAt.IsZero() {
			t.Fatalf("verified without a certificate or a time: %+v", res)
		}
	})
}

// Checkpoints are notes the log signs; only one the log signed for the
// proof's tree passes.
func FuzzCheckpoint(f *testing.F) {
	s, root, id := fuzzSigstore(f)
	b, err := ParseBundle(s.Sign(fuzzRelease, id.Subject, id.Issuer, sigstoretest.Options{RekorV2: true}))
	if err != nil {
		f.Fatal(err)
	}
	var proof *InclusionProof
	for _, e := range b.Entries {
		if e.Proof != nil && e.Proof.Checkpoint != "" {
			proof = e.Proof
		}
	}
	if proof == nil || len(root.TransparencyLogs) == 0 {
		f.Fatal("the seed bundle has no checkpoint")
	}
	signed := proof.Checkpoint
	f.Add(signed)
	f.Add("origin\n1\nAA==\n\n— log AAAA\n")
	f.Fuzz(func(t *testing.T, envelope string) {
		passed := false
		for _, log := range root.TransparencyLogs {
			passed = passed || verifyCheckpoint(envelope, log, proof) == nil
		}
		if passed {
			text, _, _ := bytes.Cut([]byte(envelope), []byte("\n\n"))
			want, _, _ := bytes.Cut([]byte(signed), []byte("\n\n"))
			if !bytes.Equal(text, want) {
				t.Fatalf("a checkpoint the log didn't sign passed: %q", envelope)
			}
		}
	})
}
