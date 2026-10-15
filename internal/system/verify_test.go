package system

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
)

// vendorKey is a signing key made for one test, with its armored public
// half.
type vendorKey struct {
	entity *openpgp.Entity
	public []byte
}

func newVendorKey(t *testing.T, name string) vendorKey {
	t.Helper()
	e, err := openpgp.NewEntity(name, "", name+"@vendor.test", nil)
	if err != nil {
		t.Fatal(err)
	}
	var b bytes.Buffer
	w, err := armor.Encode(&b, openpgp.PublicKeyType, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Serialize(w); err != nil {
		t.Fatal(err)
	}
	w.Close()
	return vendorKey{entity: e, public: b.Bytes()}
}

func (k vendorKey) sign(t *testing.T, data []byte, armored bool) []byte {
	t.Helper()
	var b bytes.Buffer
	var err error
	if armored {
		err = openpgp.ArmoredDetachSign(&b, k.entity, bytes.NewReader(data), nil)
	} else {
		err = openpgp.DetachSign(&b, k.entity, bytes.NewReader(data), nil)
	}
	if err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func sha(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}

func TestVerifySignature(t *testing.T) {
	vendor := newVendorKey(t, "vendor")
	other := newVendorKey(t, "other")
	data := []byte("node-v22.11.0-linux-x64.tar.gz contents")
	for _, armored := range []bool{true, false} {
		sig := vendor.sign(t, data, armored)
		if err := VerifySignature(vendor.public, bytes.NewReader(data), sig); err != nil {
			t.Errorf("armored=%v: %v", armored, err)
		}
		if err := VerifySignature(vendor.public, strings.NewReader("tampered"), sig); !errors.Is(err, ErrInvalid) {
			t.Errorf("armored=%v: tampered data: %v", armored, err)
		}
		if err := VerifySignature(other.public, bytes.NewReader(data), sig); !errors.Is(err, ErrInvalid) {
			t.Errorf("armored=%v: another vendor's key: %v", armored, err)
		}
	}
	if err := VerifySignature([]byte("not a key"), bytes.NewReader(data), vendor.sign(t, data, true)); !errors.Is(err, ErrInvalid) {
		t.Errorf("garbage key: %v", err)
	}
}

func TestChecksumFor(t *testing.T) {
	sum := strings.Repeat("ab", 32)
	list := []byte("# comment\n" +
		strings.Repeat("cd", 32) + "  node-v22.11.0-darwin-arm64.tar.gz\n" +
		strings.ToUpper(sum) + " *dist/node-v22.11.0-linux-x64.tar.gz\n" +
		"SHA256 (other.zip) = " + strings.Repeat("ef", 32) + "\n")
	if got, err := ChecksumFor(list, "node-v22.11.0-linux-x64.tar.gz"); err != nil || got != sum {
		t.Errorf("GNU format = %s, %v", got, err)
	}
	if got, err := ChecksumFor(list, "other.zip"); err != nil || got != strings.Repeat("ef", 32) {
		t.Errorf("BSD format = %s, %v", got, err)
	}
	if _, err := ChecksumFor(list, "missing.tar.gz"); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing entry: %v", err)
	}
	twice := append(list, []byte(strings.Repeat("00", 32)+"  other.zip\n")...)
	if _, err := ChecksumFor(twice, "other.zip"); !errors.Is(err, ErrInvalid) {
		t.Errorf("two checksums for one file: %v", err)
	}
}

func TestArchiveVerify(t *testing.T) {
	vendor := newVendorKey(t, "vendor")
	data := []byte("the archive")
	open := func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(data)), nil }
	sums := []byte(sha(data) + "  node.tar.gz\n")
	base := Archive{URL: "https://nodejs.org/dist/v22/node.tar.gz", Size: 100, Format: "tar.gz", Dest: "/opt/racksail/runtimes/node/22"}

	byChecksum := base
	byChecksum.SHA256 = sha(data)
	if err := byChecksum.Verify(sha(data), open, nil, nil); err != nil {
		t.Errorf("checksum: %v", err)
	}
	if err := byChecksum.Verify(sha([]byte("x")), open, nil, nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("checksum mismatch: %v", err)
	}

	signedList := base
	signedList.Signature = &ArchiveSignature{Key: "nodejs", URL: "https://nodejs.org/dist/v22/SHASUMS256.txt.sig", Checksums: "https://nodejs.org/dist/v22/SHASUMS256.txt", PublicKey: vendor.public}
	if err := signedList.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := signedList.Verify(sha(data), open, vendor.sign(t, sums, false), sums); err != nil {
		t.Errorf("signed checksums: %v", err)
	}
	forged := []byte(sha([]byte("evil")) + "  node.tar.gz\n")
	if err := signedList.Verify(sha([]byte("evil")), open, vendor.sign(t, sums, false), forged); !errors.Is(err, ErrInvalid) {
		t.Errorf("a list the signature doesn't cover: %v", err)
	}
	if err := signedList.Verify(sha([]byte("evil")), open, vendor.sign(t, sums, false), sums); !errors.Is(err, ErrInvalid) {
		t.Errorf("an archive the signed list doesn't match: %v", err)
	}

	signedArchive := base
	signedArchive.Signature = &ArchiveSignature{Key: "nodejs", URL: "https://nodejs.org/dist/v22/node.tar.gz.asc", PublicKey: vendor.public}
	if err := signedArchive.Verify(sha(data), open, vendor.sign(t, data, true), nil); err != nil {
		t.Errorf("signed archive: %v", err)
	}
	if err := signedArchive.Verify(sha(data), open, vendor.sign(t, []byte("other"), true), nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("a signature of something else: %v", err)
	}
	noKey := signedArchive
	noKey.Signature = &ArchiveSignature{Key: "nodejs", URL: signedArchive.Signature.URL}
	if err := noKey.Verify(sha(data), open, vendor.sign(t, data, true), nil); !errors.Is(err, ErrInvalid) {
		t.Errorf("an unresolved key: %v", err)
	}

	for _, mutate := range []func(*Archive){
		func(a *Archive) { a.SHA256, a.Signature = "", nil },
		func(a *Archive) { a.Signature = &ArchiveSignature{Key: "Bad Key", URL: "https://x.test/s"} },
		func(a *Archive) { a.Signature = &ArchiveSignature{Key: "k", URL: "http://x.test/s"} },
		func(a *Archive) {
			a.Signature = &ArchiveSignature{Key: "k", URL: "https://x.test/s", Checksums: "ftp://x.test/c"}
		},
	} {
		a := byChecksum
		mutate(&a)
		if err := a.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("Validate(%+v) should fail: %v", a, err)
		}
	}
	if (Archive{URL: "https://x.test/a/b/node.tar.gz?x=1"}).Name() != "node.tar.gz" {
		t.Error("the archive's name comes from its URL path")
	}
}

func TestTreeNames(t *testing.T) {
	for in, want := range map[string]string{"": ".", ".": ".", "a/b/../c": "a/c", "a//b/": "a/b"} {
		if in == "a/b/../c" {
			if _, err := CleanTreeName(in); !errors.Is(err, ErrInvalid) {
				t.Errorf("CleanTreeName(%q) should refuse ..: %v", in, err)
			}
			continue
		}
		if got, err := CleanTreeName(in); err != nil || got != want {
			t.Errorf("CleanTreeName(%q) = %q, %v", in, got, err)
		}
	}
	for _, bad := range []string{"/etc", "../x", "a/../../x", "a\\b", "a\x00b"} {
		if _, err := CleanTreeName(bad); !errors.Is(err, ErrInvalid) {
			t.Errorf("CleanTreeName(%q) should fail: %v", bad, err)
		}
	}
	for _, tc := range []struct {
		name, target string
		ok           bool
	}{
		{"a/link", "b", true}, {"a/link", "../b", true}, {"a/link", "../../b", false},
		{"link", "../x", false}, {"a/b/link", "../../c/d", true}, {"link", "/etc/shadow", false}, {"link", "", false},
	} {
		if got := LinkStaysInside(tc.name, tc.target); got != tc.ok {
			t.Errorf("LinkStaysInside(%q, %q) = %v", tc.name, tc.target, got)
		}
	}
	if FormatOf("site.TGZ") != FormatTarGz || FormatOf("a.tar.zst") != FormatTarZst || FormatOf("x.zip") != FormatZip || FormatOf("x.rar") != "" {
		t.Error("FormatOf guesses wrong")
	}
}

func TestAllocateAccountID(t *testing.T) {
	users := []User{{Name: "root", UID: 0}, {Name: "blog", UID: 20000, GID: 20000}, {Name: "x", UID: 20002, GID: 20002}}
	groups := []Group{{Name: "shared", GID: 20001}}
	if id, err := AllocateAccountID(users, groups, nil); err != nil || id != 20003 {
		t.Errorf("first free = %d, %v", id, err)
	}
	if id, err := AllocateAccountID(users, groups, []int{20003, 20004}); err != nil || id != 20005 {
		t.Errorf("with exclusions = %d, %v", id, err)
	}
	all := make([]int, 0, AccountUIDMax-AccountUIDMin+1)
	for id := AccountUIDMin; id <= AccountUIDMax; id++ {
		all = append(all, id)
	}
	if _, err := AllocateAccountID(nil, nil, all); !errors.Is(err, ErrExists) {
		t.Errorf("a full range: %v", err)
	}
}

func TestJournalQuery(t *testing.T) {
	warning := PriorityWarning
	q := JournalQuery{Units: []string{"nginx.service"}, Priority: &warning, Grep: "Timeout"}
	if err := q.Validate(); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if !q.Matches(JournalEntry{Unit: "nginx.service", Priority: PriorityError, Message: "upstream timeout", Time: now}) {
		t.Error("an error mentioning a timeout matches")
	}
	if q.Matches(JournalEntry{Unit: "nginx.service", Priority: PriorityInfo, Message: "timeout"}) {
		t.Error("info is less severe than warning")
	}
	if q.Matches(JournalEntry{Unit: "other.service", Priority: PriorityError, Message: "timeout"}) {
		t.Error("another unit")
	}
	bad := 9
	for _, b := range []JournalQuery{
		{Units: []string{"bad"}}, {Namespace: "Bad NS"}, {Priority: &bad}, {Grep: "a\nb"}, {After: "x y"},
		{Limit: MaxJournalLimit + 1}, {Since: now, Until: now.Add(-time.Hour)},
	} {
		if err := b.Validate(); !errors.Is(err, ErrInvalid) {
			t.Errorf("Validate(%+v) should fail: %v", b, err)
		}
	}
	if (JournalQuery{}).EffectiveLimit() != DefaultJournalLimit {
		t.Error("default limit")
	}
}
