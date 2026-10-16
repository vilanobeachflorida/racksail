// Package sigstoretest stands in for Sigstore in tests: a certificate
// authority, transparency logs, a certificate transparency log and a
// timestamp authority of its own, the trusted root that lists them, and
// bundles signed the way cosign sign-blob signs.
package sigstoretest

import (
	"bytes"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/url"
	"sync"
	"time"
)

// Sigstore is a private Sigstore.
type Sigstore struct {
	rootKey, caKey  *ecdsa.PrivateKey
	Root, CA        *x509.Certificate
	ctKey, rekorKey *ecdsa.PrivateKey
	rekor2Key       ed25519.PrivateKey
	tsaRootKey      *ecdsa.PrivateKey
	tsaKey          *ecdsa.PrivateKey
	TSARoot, TSA    *x509.Certificate

	mu    sync.Mutex
	index int64
}

func must[T any](v T, err error) T {
	if err != nil {
		panic(err)
	}
	return v
}

func newKey(curve elliptic.Curve) *ecdsa.PrivateKey {
	return must(ecdsa.GenerateKey(curve, rand.Reader))
}

func serial() *big.Int { return must(rand.Int(rand.Reader, big.NewInt(1<<62))) }

// New starts a private Sigstore whose authorities are valid from a year
// ago for twenty years.
func New() *Sigstore {
	s := &Sigstore{
		rootKey: newKey(elliptic.P384()), caKey: newKey(elliptic.P384()),
		ctKey: newKey(elliptic.P256()), rekorKey: newKey(elliptic.P256()),
		tsaRootKey: newKey(elliptic.P384()), tsaKey: newKey(elliptic.P256()),
		index: 1000,
	}
	_, s.rekor2Key = must2(ed25519.GenerateKey(rand.Reader))
	start := time.Now().AddDate(-1, 0, 0)
	end := start.AddDate(20, 0, 0)
	ca := func(name string, key, parentKey *ecdsa.PrivateKey, parent *x509.Certificate) *x509.Certificate {
		tmpl := &x509.Certificate{
			SerialNumber: serial(), Subject: pkix.Name{CommonName: name, Organization: []string{"sigstore.test"}},
			NotBefore: start, NotAfter: end, IsCA: true, BasicConstraintsValid: true,
			KeyUsage:    x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
			ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		}
		if parent == nil {
			parent, parentKey = tmpl, key
		}
		return must(x509.ParseCertificate(must(x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey))))
	}
	s.Root = ca("sigstore", s.rootKey, nil, nil)
	s.CA = ca("sigstore-intermediate", s.caKey, s.rootKey, s.Root)
	tsaRoot := &x509.Certificate{
		SerialNumber: serial(), Subject: pkix.Name{CommonName: "tsa root"}, NotBefore: start, NotAfter: end,
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	s.TSARoot = must(x509.ParseCertificate(must(x509.CreateCertificate(rand.Reader, tsaRoot, tsaRoot, &s.tsaRootKey.PublicKey, s.tsaRootKey))))
	tsa := &x509.Certificate{
		SerialNumber: serial(), Subject: pkix.Name{CommonName: "tsa"}, NotBefore: start, NotAfter: end,
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageTimeStamping},
	}
	s.TSA = must(x509.ParseCertificate(must(x509.CreateCertificate(rand.Reader, tsa, s.TSARoot, &s.tsaKey.PublicKey, s.tsaRootKey))))
	return s
}

func must2[A, B any](a A, b B, err error) (A, B) {
	if err != nil {
		panic(err)
	}
	return a, b
}

func spki(key crypto.PublicKey) []byte { return must(x509.MarshalPKIXPublicKey(key)) }

func logID(key crypto.PublicKey) []byte {
	sum := sha256.Sum256(spki(key))
	return sum[:]
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// TrustedRoot is the trusted root that lists the private Sigstore, in the
// JSON form Sigstore publishes.
func (s *Sigstore) TrustedRoot() []byte {
	start := time.Now().AddDate(-1, 0, 0).UTC().Format(time.RFC3339)
	key := func(k crypto.PublicKey, details string) map[string]any {
		return map[string]any{"rawBytes": b64(spki(k)), "keyDetails": details, "validFor": map[string]string{"start": start}}
	}
	chain := func(certs ...*x509.Certificate) map[string]any {
		var list []map[string]string
		for _, c := range certs {
			list = append(list, map[string]string{"rawBytes": b64(c.Raw)})
		}
		return map[string]any{"certificates": list}
	}
	root := map[string]any{
		"mediaType": "application/vnd.dev.sigstore.trustedroot+json;version=0.1",
		"tlogs": []any{
			map[string]any{"baseUrl": "https://rekor.sigstore.test", "hashAlgorithm": "SHA2_256", "publicKey": key(&s.rekorKey.PublicKey, "PKIX_ECDSA_P256_SHA_256"), "logId": map[string]string{"keyId": b64(logID(&s.rekorKey.PublicKey))}},
			map[string]any{"baseUrl": "https://log2.rekor.sigstore.test", "hashAlgorithm": "SHA2_256", "publicKey": key(s.rekor2Key.Public(), "PKIX_ED25519"), "logId": map[string]string{"keyId": b64(logID(s.rekor2Key.Public()))}},
		},
		"certificateAuthorities": []any{map[string]any{"uri": "https://fulcio.sigstore.test", "certChain": chain(s.CA, s.Root), "validFor": map[string]string{"start": start}}},
		"ctlogs":                 []any{map[string]any{"baseUrl": "https://ctfe.sigstore.test", "hashAlgorithm": "SHA2_256", "publicKey": key(&s.ctKey.PublicKey, "PKIX_ECDSA_P256_SHA_256"), "logId": map[string]string{"keyId": b64(logID(&s.ctKey.PublicKey))}}},
		"timestampAuthorities":   []any{map[string]any{"uri": "https://tsa.sigstore.test", "certChain": chain(s.TSA, s.TSARoot), "validFor": map[string]string{"start": start}}},
	}
	return must(json.Marshal(root))
}

// Options change how a bundle is signed, to make bundles that must fail.
type Options struct {
	// RekorV2 logs in the second log, which signs checkpoints with Ed25519
	// and promises nothing, and timestamps the signature instead.
	RekorV2 bool
	// NoSCT leaves the certificate transparency timestamp out.
	NoSCT bool
	// Expired makes the certificate expire before the file was logged.
	Expired bool
}

var (
	oidSCTList  = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 11129, 2, 4, 2}
	oidIssuerV1 = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 1}
	oidIssuerV2 = asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 1, 8}
)

// Sign signs content as identity, vouched for by issuer, and returns the
// bundle.
func (s *Sigstore) Sign(content []byte, identity, issuer string, opts Options) []byte {
	digest := sha256.Sum256(content)
	return s.SignDigest(digest[:], identity, issuer, opts)
}

// SignDigest signs the file with the given SHA-256.
func (s *Sigstore) SignDigest(digest []byte, identity, issuer string, opts Options) []byte {
	key := newKey(elliptic.P256())
	sig := must(ecdsa.SignASN1(rand.Reader, key, digest))
	cert := s.certificate(key, identity, issuer, opts)
	now := time.Now()
	var entry map[string]any
	var timestamps []any
	if opts.RekorV2 {
		body := must(json.Marshal(map[string]any{
			"kind": "hashedrekord", "apiVersion": "0.0.2",
			"spec": map[string]any{"hashedRekordV002": map[string]any{
				"data": map[string]string{"algorithm": "SHA2_256", "digest": b64(digest)},
				"signature": map[string]any{"content": b64(sig), "verifier": map[string]any{
					"x509Certificate": map[string]string{"rawBytes": b64(cert.Raw)}, "keyDetails": "PKIX_ECDSA_P256_SHA_256",
				}},
			}},
		}))
		entry = s.entry(body, "0.0.2", 0, nil, s.rekor2Key, logID(s.rekor2Key.Public()), "log2.rekor.sigstore.test")
		timestamps = append(timestamps, map[string]string{"signedTimestamp": b64(s.Timestamp(sig, now))})
	} else {
		pemCert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})
		body := must(json.Marshal(map[string]any{
			"apiVersion": "0.0.1", "kind": "hashedrekord",
			"spec": map[string]any{
				"data":      map[string]any{"hash": map[string]string{"algorithm": "sha256", "value": hex.EncodeToString(digest)}},
				"signature": map[string]any{"content": b64(sig), "publicKey": map[string]string{"content": b64(pemCert)}},
			},
		}))
		entry = s.entry(body, "0.0.1", now.Unix(), s.rekorKey, nil, logID(&s.rekorKey.PublicKey), "rekor.sigstore.test")
	}
	vm := map[string]any{"certificate": map[string]string{"rawBytes": b64(cert.Raw)}, "tlogEntries": []any{entry}}
	if timestamps != nil {
		vm["timestampVerificationData"] = map[string]any{"rfc3161Timestamps": timestamps}
	}
	return must(json.Marshal(map[string]any{
		"mediaType":            "application/vnd.dev.sigstore.bundle.v0.3+json",
		"verificationMaterial": vm,
		"messageSignature":     map[string]any{"messageDigest": map[string]string{"algorithm": "SHA2_256", "digest": b64(digest)}, "signature": b64(sig)},
	}))
}

func (s *Sigstore) certificate(key *ecdsa.PrivateKey, identity, issuer string, opts Options) *x509.Certificate {
	u := must(url.Parse(identity))
	start := time.Now().Add(-time.Minute)
	end := time.Now().Add(10 * time.Minute)
	if opts.Expired {
		start, end = time.Now().Add(-time.Hour), time.Now().Add(-50*time.Minute)
	}
	issuerV2 := must(asn1.MarshalWithParams(issuer, "utf8"))
	tmpl := &x509.Certificate{
		SerialNumber: serial(), NotBefore: start, NotAfter: end, URIs: []*url.URL{u},
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageCodeSigning},
		ExtraExtensions: []pkix.Extension{{Id: oidIssuerV1, Value: []byte(issuer)}, {Id: oidIssuerV2, Value: issuerV2}},
	}
	if opts.NoSCT {
		return must(x509.ParseCertificate(must(x509.CreateCertificate(rand.Reader, tmpl, s.CA, &key.PublicKey, s.caKey))))
	}
	precert := must(x509.ParseCertificate(must(x509.CreateCertificate(rand.Reader, tmpl, s.CA, &key.PublicKey, s.caKey))))
	ts := uint64(time.Now().UnixMilli())
	issuerHash := sha256.Sum256(s.CA.RawSubjectPublicKeyInfo)
	tbs := precert.RawTBSCertificate
	var signed bytes.Buffer
	signed.Write([]byte{0, 0})
	binary.Write(&signed, binary.BigEndian, ts)
	signed.Write([]byte{0, 1})
	signed.Write(issuerHash[:])
	signed.Write([]byte{byte(len(tbs) >> 16), byte(len(tbs) >> 8), byte(len(tbs))})
	signed.Write(tbs)
	signed.Write([]byte{0, 0})
	sum := sha256.Sum256(signed.Bytes())
	sctSig := must(ecdsa.SignASN1(rand.Reader, s.ctKey, sum[:]))
	var sct bytes.Buffer
	sct.WriteByte(0)
	sct.Write(logID(&s.ctKey.PublicKey))
	binary.Write(&sct, binary.BigEndian, ts)
	sct.Write([]byte{0, 0})
	sct.Write([]byte{4, 3})
	binary.Write(&sct, binary.BigEndian, uint16(len(sctSig)))
	sct.Write(sctSig)
	var list bytes.Buffer
	binary.Write(&list, binary.BigEndian, uint16(sct.Len()+2))
	binary.Write(&list, binary.BigEndian, uint16(sct.Len()))
	list.Write(sct.Bytes())
	tmpl.ExtraExtensions = append(tmpl.ExtraExtensions, pkix.Extension{Id: oidSCTList, Value: must(asn1.Marshal(list.Bytes()))})
	return must(x509.ParseCertificate(must(x509.CreateCertificate(rand.Reader, tmpl, s.CA, &key.PublicKey, s.caKey))))
}

// entry logs body in a tree with a few other entries and returns the log
// entry a bundle carries.
func (s *Sigstore) entry(body []byte, version string, integrated int64, ecKey *ecdsa.PrivateKey, edKey ed25519.PrivateKey, id []byte, origin string) map[string]any {
	s.mu.Lock()
	s.index++
	global := s.index
	s.mu.Unlock()
	leaves := [][]byte{[]byte("first"), []byte("second"), []byte("third"), body, []byte("fifth"), []byte("sixth")}
	const at = 3
	hashes := make([][]byte, len(leaves))
	for i, l := range leaves {
		hashes[i] = leafHash(l)
	}
	root := treeHash(hashes)
	var proof []string
	for _, h := range auditPath(at, hashes) {
		proof = append(proof, b64(h))
	}
	text := fmt.Sprintf("%s - 2605736670972794746\n%d\n%s\n", origin, len(leaves), b64(root))
	var noteSig []byte
	if edKey != nil {
		noteSig = ed25519.Sign(edKey, []byte(text))
	} else {
		sum := sha256.Sum256([]byte(text))
		noteSig = must(ecdsa.SignASN1(rand.Reader, ecKey, sum[:]))
	}
	envelope := text + "\n— " + origin + " " + b64(append(id[:4:4], noteSig...)) + "\n"
	e := map[string]any{
		"logIndex": fmt.Sprint(global), "logId": map[string]string{"keyId": b64(id)},
		"kindVersion": map[string]string{"kind": "hashedrekord", "version": version},
		"inclusionProof": map[string]any{
			"logIndex": fmt.Sprint(at), "rootHash": b64(root), "treeSize": fmt.Sprint(len(leaves)), "hashes": proof,
			"checkpoint": map[string]string{"envelope": envelope},
		},
		"canonicalizedBody": b64(body),
	}
	if integrated != 0 {
		e["integratedTime"] = fmt.Sprint(integrated)
		payload := fmt.Sprintf(`{"body":%q,"integratedTime":%d,"logID":%q,"logIndex":%d}`, b64(body), integrated, hex.EncodeToString(id), global)
		sum := sha256.Sum256([]byte(payload))
		e["inclusionPromise"] = map[string]string{"signedEntryTimestamp": b64(must(ecdsa.SignASN1(rand.Reader, ecKey, sum[:])))}
	}
	return e
}

func leafHash(b []byte) []byte {
	h := sha256.New()
	h.Write([]byte{0})
	h.Write(b)
	return h.Sum(nil)
}

func node(l, r []byte) []byte {
	h := sha256.New()
	h.Write([]byte{1})
	h.Write(l)
	h.Write(r)
	return h.Sum(nil)
}

// split is the largest power of two smaller than n.
func split(n int) int {
	k := 1
	for k<<1 < n {
		k <<= 1
	}
	return k
}

func treeHash(leaves [][]byte) []byte {
	if len(leaves) == 1 {
		return leaves[0]
	}
	k := split(len(leaves))
	return node(treeHash(leaves[:k]), treeHash(leaves[k:]))
}

// auditPath is RFC 6962's PATH(m, D[n]).
func auditPath(m int, leaves [][]byte) [][]byte {
	if len(leaves) <= 1 {
		return nil
	}
	k := split(len(leaves))
	if m < k {
		return append(auditPath(m, leaves[:k]), treeHash(leaves[k:]))
	}
	return append(auditPath(m-k, leaves[k:]), treeHash(leaves[:k]))
}

var (
	oidSignedData    = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	oidTSTInfo       = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 16, 1, 4}
	oidContentType   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 3}
	oidMessageDigest = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 4}
	oidSHA256        = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
	oidECDSASHA256   = asn1.ObjectIdentifier{1, 2, 840, 10045, 4, 3, 2}
)

type imprint struct {
	HashAlgorithm pkix.AlgorithmIdentifier
	HashedMessage []byte
}

type tstInfo struct {
	Version        int
	Policy         asn1.ObjectIdentifier
	MessageImprint imprint
	SerialNumber   *big.Int
	GenTime        time.Time `asn1:"generalized"`
}

type attribute struct {
	Type   asn1.ObjectIdentifier
	Values asn1.RawValue
}

type issuerSerial struct {
	Issuer asn1.RawValue
	Serial *big.Int
}

type signerInfo struct {
	Version            int
	SID                asn1.RawValue
	DigestAlgorithm    pkix.AlgorithmIdentifier
	SignedAttrs        asn1.RawValue
	SignatureAlgorithm pkix.AlgorithmIdentifier
	Signature          []byte
}

type encapsulated struct {
	EContentType asn1.ObjectIdentifier
	EContent     asn1.RawValue
}

type signedData struct {
	Version          int
	DigestAlgorithms asn1.RawValue
	EncapContentInfo encapsulated
	Certificates     asn1.RawValue
	SignerInfos      []signerInfo `asn1:"set"`
}

type contentInfo struct {
	ContentType asn1.ObjectIdentifier
	Content     asn1.RawValue
}

type statusInfo struct {
	Status int
}

type timeStampResp struct {
	Status statusInfo
	Token  contentInfo
}

func set(values ...[]byte) asn1.RawValue {
	return asn1.RawValue{Class: asn1.ClassUniversal, Tag: asn1.TagSet, IsCompound: true, Bytes: bytes.Join(values, nil)}
}

func explicit(tag int, inner []byte) asn1.RawValue {
	return asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: tag, IsCompound: true, Bytes: inner}
}

// Timestamp is an RFC 3161 timestamp response from the private timestamp
// authority over sig, at a time.
func (s *Sigstore) Timestamp(sig []byte, at time.Time) []byte {
	sum := sha256.Sum256(sig)
	info := must(asn1.Marshal(tstInfo{
		Version: 1, Policy: asn1.ObjectIdentifier{1, 3, 6, 1, 4, 1, 57264, 2},
		MessageImprint: imprint{HashAlgorithm: pkix.AlgorithmIdentifier{Algorithm: oidSHA256}, HashedMessage: sum[:]},
		SerialNumber:   serial(), GenTime: at.UTC().Truncate(time.Second),
	}))
	infoSum := sha256.Sum256(info)
	attrs := must(asn1.MarshalWithParams([]attribute{
		{Type: oidContentType, Values: set(must(asn1.Marshal(oidTSTInfo)))},
		{Type: oidMessageDigest, Values: set(must(asn1.Marshal(infoSum[:])))},
	}, "set"))
	attrsSum := sha256.Sum256(attrs)
	signature := must(ecdsa.SignASN1(rand.Reader, s.tsaKey, attrsSum[:]))
	signedAttrs := append([]byte{0xA0}, attrs[1:]...)
	sid := must(asn1.Marshal(issuerSerial{Issuer: asn1.RawValue{FullBytes: s.TSA.RawIssuer}, Serial: s.TSA.SerialNumber}))
	sd := must(asn1.Marshal(signedData{
		Version:          3,
		DigestAlgorithms: set(must(asn1.Marshal(pkix.AlgorithmIdentifier{Algorithm: oidSHA256}))),
		EncapContentInfo: encapsulated{EContentType: oidTSTInfo, EContent: explicit(0, must(asn1.Marshal(info)))},
		Certificates:     asn1.RawValue{Class: asn1.ClassContextSpecific, Tag: 0, IsCompound: true, Bytes: s.TSA.Raw},
		SignerInfos: []signerInfo{{
			Version: 1, SID: asn1.RawValue{FullBytes: sid}, DigestAlgorithm: pkix.AlgorithmIdentifier{Algorithm: oidSHA256},
			SignedAttrs: asn1.RawValue{FullBytes: signedAttrs}, SignatureAlgorithm: pkix.AlgorithmIdentifier{Algorithm: oidECDSASHA256},
			Signature: signature,
		}},
	}))
	return must(asn1.Marshal(timeStampResp{Token: contentInfo{ContentType: oidSignedData, Content: explicit(0, sd)}}))
}

// Identity is the identity Racksail's release workflow signs as for a tag.
func Identity(tag string) string {
	return "https://github.com/vilanobeachflorida/racksail/.github/workflows/release.yml@refs/tags/" + tag
}

// Issuer is GitHub Actions' token issuer.
const Issuer = "https://token.actions.githubusercontent.com"
