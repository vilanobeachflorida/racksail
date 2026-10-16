package identity

import (
	"context"
	"crypto/ecdsa"
	"crypto/rsa"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/ssh"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/changeset"
	"github.com/vilanobeachflorida/racksail/internal/i18n"
	"github.com/vilanobeachflorida/racksail/internal/store"
)

// maxSSHKeys is how many SSH keys one user may have.
const maxSSHKeys = 50

// minRSABits is the smallest RSA key accepted; smaller ones are within
// reach of attackers.
const minRSABits = 2048

// acceptedKeyTypes are the SSH key types Racksail accepts. DSA keys are
// broken, and OpenSSH no longer accepts them either.
var acceptedKeyTypes = []string{
	ssh.KeyAlgoED25519, ssh.KeyAlgoSKED25519, ssh.KeyAlgoECDSA256, ssh.KeyAlgoECDSA384, ssh.KeyAlgoECDSA521,
	ssh.KeyAlgoSKECDSA256, ssh.KeyAlgoRSA,
}

// ParseSSHKey reads a public key as in an authorized_keys line and returns
// it as Racksail shows it, with the comment that followed it. Options such
// as command= aren't accepted: what a key may do is Racksail's to decide.
func ParseSSHKey(line string) (*SSHKey, string, error) {
	line = strings.TrimSpace(line)
	if strings.Contains(line, "\n") {
		return nil, "", auth.ErrSSHKeyInvalid.New("reason", "paste one key at a time")
	}
	key, comment, options, rest, err := ssh.ParseAuthorizedKey([]byte(line))
	switch {
	case err != nil:
		return nil, "", auth.ErrSSHKeyInvalid.New("reason", "it doesn't look like an SSH public key")
	case len(options) > 0:
		return nil, "", auth.ErrSSHKeyInvalid.New("reason", "remove the options in front of the key")
	case len(strings.TrimSpace(string(rest))) > 0:
		return nil, "", auth.ErrSSHKeyInvalid.New("reason", "paste one key at a time")
	}
	typ := key.Type()
	accepted := false
	for _, t := range acceptedKeyTypes {
		if t == typ {
			accepted = true
		}
	}
	if !accepted {
		return nil, "", auth.ErrSSHKeyInvalid.New("reason", typ+" keys aren't accepted; use an Ed25519 key")
	}
	var bits int
	if ck, ok := key.(ssh.CryptoPublicKey); ok {
		switch pk := ck.CryptoPublicKey().(type) {
		case *rsa.PublicKey:
			bits = pk.N.BitLen()
			if bits < minRSABits {
				return nil, "", auth.ErrSSHKeyInvalid.New("reason", "RSA keys need at least 2048 bits; use an Ed25519 key")
			}
		case *ecdsa.PublicKey:
			bits = pk.Curve.Params().BitSize
		}
	}
	authorized := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(key)))
	fingerprint := ssh.FingerprintSHA256(key)
	return &SSHKey{ID: keyID(fingerprint), Type: typ, Bits: bits, Fingerprint: fingerprint, PublicKey: authorized}, strings.TrimSpace(comment), nil
}

// newSSHKey turns what someone gave into the key a user's settings keep:
// named after its comment, or its type, when they gave no name.
func newSSHKey(in SSHKeyInput) (UserSSHKey, error) {
	key, comment, err := ParseSSHKey(in.PublicKey)
	if err != nil {
		return UserSSHKey{}, i18n.Invalid(i18n.Field("input.publicKey", auth.ErrSSHKeyInvalid, "reason", i18n.From(err).Params["reason"]))
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = comment
	}
	if name == "" {
		name = key.Type
	}
	if utf8.RuneCountInString(name) > 64 || strings.ContainsAny(name, "\x00\n\r") {
		return UserSSHKey{}, i18n.Invalid(i18n.Field("input.name", i18n.ErrBadValue, "reason", "use at most 64 characters on one line"))
	}
	return UserSSHKey{ID: key.ID, Name: name, PublicKey: key.PublicKey}, nil
}

// sshKeyViews are a user's SSH keys as the panel shows them.
func sshKeyViews(u *User) []SSHKey {
	out := make([]SSHKey, 0, len(u.SSHKeys))
	for _, k := range u.SSHKeys {
		v, _, err := ParseSSHKey(k.PublicKey)
		if err != nil {
			// A key a newer release stopped accepting still shows, so it
			// can be removed.
			v = &SSHKey{ID: k.ID, PublicKey: k.PublicKey}
		}
		v.ID, v.User, v.Name = k.ID, u.ID, k.Name
		out = append(out, *v)
	}
	return out
}

// MySSHKeys lists the actor's SSH keys.
func (s *Service) MySSHKeys(ctx context.Context, actor *auth.Actor) ([]SSHKey, error) {
	u, err := s.User(ctx, actor.Person())
	if err != nil {
		return nil, err
	}
	return sshKeyViews(u), nil
}

// UserSSHKeys lists another user's SSH keys, for those who may see them.
func (s *Service) UserSSHKeys(ctx context.Context, actor *auth.Actor, id store.ID) ([]SSHKey, error) {
	u, err := s.visible(ctx, actor, id, auth.PermUserRead)
	if err != nil {
		return nil, err
	}
	return sshKeyViews(u), nil
}

// ownChange checks that a change of the actor's own user is theirs to make
// in the panel: not an API token's, and not someone's viewing the panel as
// them.
func ownChange(actor *auth.Actor, action string) error {
	if err := requirePerson(actor); err != nil {
		return err
	}
	if actor.Impersonator != "" {
		return auth.ErrImpersonating.New("user", actor.Name, "action", action)
	}
	return nil
}

// AddSSHKey is the change that adds an SSH key to the actor's own user.
func (s *Service) AddSSHKey(actor *auth.Actor, in SSHKeyInput) (changeset.Request, error) {
	if err := ownChange(actor, "add SSH keys"); err != nil {
		return changeset.Request{}, err
	}
	key, err := newSSHKey(in)
	if err != nil {
		return changeset.Request{}, err
	}
	input, err := json.Marshal(SSHKeyInput{Name: key.Name, PublicKey: key.PublicKey})
	if err != nil {
		return changeset.Request{}, err
	}
	return changeset.Request{
		Title:   "Add the SSH key " + key.Name,
		Changes: []changeset.Change{{Op: changeset.OpAction, Kind: KindUser, ID: actor.Person(), Action: "add_ssh_key", Input: input}},
	}, nil
}

// RemoveSSHKey is the change that removes one of the actor's own SSH keys.
func (s *Service) RemoveSSHKey(ctx context.Context, actor *auth.Actor, id store.ID) (changeset.Request, error) {
	if err := ownChange(actor, "remove SSH keys"); err != nil {
		return changeset.Request{}, err
	}
	u, err := s.User(ctx, actor.Person())
	if err != nil {
		return changeset.Request{}, err
	}
	var name string
	for _, k := range u.SSHKeys {
		if k.ID == id {
			name = k.Name
		}
	}
	if name == "" {
		return changeset.Request{}, notFound(string(id))
	}
	input, err := json.Marshal(SSHKeyRef{ID: id})
	if err != nil {
		return changeset.Request{}, err
	}
	return changeset.Request{
		Title:   "Remove the SSH key " + name,
		Changes: []changeset.Change{{Op: changeset.OpAction, Kind: KindUser, ID: u.ID, Action: "remove_ssh_key", Input: input}},
	}, nil
}
