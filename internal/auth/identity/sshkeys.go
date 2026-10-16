package identity

import (
	"context"
	"crypto/ecdsa"
	"crypto/rsa"
	"strings"

	"golang.org/x/crypto/ssh"

	"github.com/vilanobeachflorida/racksail/internal/auth"
	"github.com/vilanobeachflorida/racksail/internal/events"
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

// SSHKeyInput is a new SSH key.
type SSHKeyInput struct {
	Name string `json:"name,omitempty" maxLength:"64" doc:"What the key is for; the key's comment when left out"`
	// PublicKey is the key as in an authorized_keys file or a .pub file.
	PublicKey string `json:"publicKey" minLength:"1" maxLength:"16384" doc:"The public key, as in a .pub file or an authorized_keys line, without options"`
}

// ParseSSHKey reads a public key as in an authorized_keys line and returns
// it as Racksail keeps it. Options such as command= aren't accepted: what
// a key may do is Racksail's to decide.
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
	return &SSHKey{Type: typ, Bits: bits, Fingerprint: ssh.FingerprintSHA256(key), PublicKey: authorized}, strings.TrimSpace(comment), nil
}

// MySSHKeys lists the actor's SSH keys.
func (s *Service) MySSHKeys(ctx context.Context, actor *auth.Actor) ([]SSHKey, error) {
	return s.sshKeysOf(ctx, actor.Person())
}

func (s *Service) sshKeysOf(ctx context.Context, user store.ID) ([]SSHKey, error) {
	keys, err := s.opts.Store.SSHKeys(ctx, user)
	if err != nil {
		return nil, err
	}
	out := make([]SSHKey, len(keys))
	for i, k := range keys {
		out[i] = *k
	}
	return out, nil
}

// AddSSHKey adds an SSH key to the actor's user.
func (s *Service) AddSSHKey(ctx context.Context, actor *auth.Actor, in SSHKeyInput) (SSHKey, error) {
	if actor.Impersonator != "" {
		return SSHKey{}, auth.ErrImpersonating.New("user", actor.Name, "action", "add SSH keys")
	}
	u, err := s.opts.Store.User(ctx, actor.Person())
	if err != nil {
		return SSHKey{}, err
	}
	return s.addSSHKey(ctx, actor, u, in)
}

func (s *Service) addSSHKey(ctx context.Context, actor *auth.Actor, u *User, in SSHKeyInput) (SSHKey, error) {
	key, comment, err := ParseSSHKey(in.PublicKey)
	if err != nil {
		return SSHKey{}, i18n.Invalid(i18n.Field("publicKey", auth.ErrSSHKeyInvalid, "reason", i18n.From(err).Params["reason"]))
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = comment
	}
	if name == "" {
		name = key.Type
	}
	if len(name) > 64 || strings.ContainsAny(name, "\x00\n\r") {
		return SSHKey{}, i18n.Invalid(i18n.Field("name", i18n.ErrBadValue, "reason", "use at most 64 characters on one line"))
	}
	existing, err := s.opts.Store.SSHKeys(ctx, u.ID)
	if err != nil {
		return SSHKey{}, err
	}
	if len(existing) >= maxSSHKeys {
		return SSHKey{}, auth.ErrTooMany.New("count", itoa(len(existing)), "what", "SSH keys")
	}
	key.ID, key.User, key.Name, key.CreatedAt = store.NewID(PrefixSSHKey), u.ID, name, s.now()
	if err := s.opts.Store.AddSSHKey(ctx, key); err != nil {
		return SSHKey{}, err
	}
	s.audit(ctx, actor, "core.user.add_ssh_key", u.ID, u.Home, events.OutcomeSuccess, "", "Add the SSH key "+name+" ("+key.Fingerprint+")")
	s.publish(ctx, UserSSHKeyChanged, u, actor, u.Username+" added an SSH key.", UserEvent{Username: u.Username, Detail: "added " + key.Fingerprint})
	s.alert(ctx, u, "An SSH key was added", "The SSH key "+name+" ("+key.Fingerprint+") was added to "+u.Username+". If you didn't add it, remove it from your settings.", events.SeverityNotice)
	return *key, nil
}

// RemoveSSHKey removes one of the actor's SSH keys.
func (s *Service) RemoveSSHKey(ctx context.Context, actor *auth.Actor, id store.ID) error {
	if actor.Impersonator != "" {
		return auth.ErrImpersonating.New("user", actor.Name, "action", "remove SSH keys")
	}
	u, err := s.opts.Store.User(ctx, actor.Person())
	if err != nil {
		return err
	}
	return s.removeSSHKey(ctx, actor, u, id)
}

func (s *Service) removeSSHKey(ctx context.Context, actor *auth.Actor, u *User, id store.ID) error {
	keys, err := s.opts.Store.SSHKeys(ctx, u.ID)
	if err != nil {
		return err
	}
	var key *SSHKey
	for _, k := range keys {
		if k.ID == id {
			key = k
		}
	}
	if key == nil {
		return notFound(string(id))
	}
	if err := s.opts.Store.DeleteSSHKey(ctx, u.ID, id); err != nil {
		return err
	}
	s.audit(ctx, actor, "core.user.remove_ssh_key", u.ID, u.Home, events.OutcomeSuccess, "", "Remove the SSH key "+key.Name+" ("+key.Fingerprint+")")
	s.publish(ctx, UserSSHKeyChanged, u, actor, u.Username+" removed an SSH key.", UserEvent{Username: u.Username, Detail: "removed " + key.Fingerprint})
	return nil
}

// UserSSHKeys lists another user's SSH keys.
func (s *Service) UserSSHKeys(ctx context.Context, actor *auth.Actor, id store.ID) ([]SSHKey, error) {
	u, err := s.visible(ctx, actor, id, auth.PermUserRead)
	if err != nil {
		return nil, err
	}
	return s.sshKeysOf(ctx, u.ID)
}

// RemoveUserSSHKey removes another user's SSH key, for whoever may help
// them.
func (s *Service) RemoveUserSSHKey(ctx context.Context, actor *auth.Actor, user, id store.ID) error {
	u, err := s.visible(ctx, actor, user, auth.PermUserReset)
	if err != nil {
		return err
	}
	return s.removeSSHKey(ctx, actor, u, id)
}
