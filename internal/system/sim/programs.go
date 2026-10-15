package sim

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"regexp"
	"strings"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

// Programs every simulated Debian server has. Modules simulate the programs
// of the services they manage themselves.
func init() {
	RegisterProgram("/usr/bin/timedatectl", timedatectl)
	RegisterProgram("/usr/bin/systemd-creds", systemdCreds)
}

// exitWith is a program that failed the way the real one reports it.
func exitWith(code int, format string, args ...any) (system.Output, error) {
	return system.Output{ExitCode: code, Stderr: []byte(fmt.Sprintf(format, args...) + "\n")}, nil
}

// splitArgs separates --options from operands.
func splitArgs(args []string) (opts map[string]string, operands []string) {
	opts = map[string]string{}
	for _, a := range args {
		if strings.HasPrefix(a, "--") {
			k, v, _ := strings.Cut(strings.TrimPrefix(a, "--"), "=")
			opts[k] = v
			continue
		}
		operands = append(operands, a)
	}
	return opts, operands
}

var zonePattern = regexp.MustCompile(`^(UTC|Etc/UTC|[A-Z][A-Za-z_-]+(/[A-Za-z0-9_+-]+){1,2})$`)

func timedatectl(ctx context.Context, m *Machine, cmd system.Command) (system.Output, error) {
	if len(cmd.Args) == 0 {
		return exitWith(1, "timedatectl needs a command in the simulator, such as set-timezone or show.")
	}
	switch cmd.Args[0] {
	case "set-timezone":
		if len(cmd.Args) != 2 {
			return exitWith(1, "Invalid number of arguments.")
		}
		zone := cmd.Args[1]
		if !zonePattern.MatchString(zone) {
			return exitWith(1, "Failed to set time zone: Invalid or not installed time zone '%s'", zone)
		}
		if err := m.lock(ctx); err != nil {
			return system.Output{}, err
		}
		defer m.mu.Unlock()
		if err := m.record("time", "set_timezone", zone); err != nil {
			return system.Output{}, err
		}
		if m.facts.Timezone != zone {
			m.facts.Timezone = zone
			m.logLocked("systemd-timedated.service", "systemd-timedated", 0, system.PriorityInfo, "Changed time zone to '"+zone+"'.")
		}
		return system.Output{}, nil
	case "show":
		if err := m.lock(ctx); err != nil {
			return system.Output{}, err
		}
		props := []string{"Timezone=" + m.facts.Timezone, "LocalRTC=no", "CanNTP=yes", "NTP=yes", "NTPSynchronized=yes"}
		m.mu.Unlock()
		opts, _ := splitArgs(cmd.Args[1:])
		var out strings.Builder
		for i := 1; i < len(cmd.Args); i++ {
			if cmd.Args[i] == "-p" && i+1 < len(cmd.Args) {
				opts["property"] = cmd.Args[i+1]
			}
		}
		for _, p := range props {
			k, v, _ := strings.Cut(p, "=")
			if want, ok := opts["property"]; ok && want != k {
				continue
			}
			if _, ok := opts["value"]; ok {
				out.WriteString(v + "\n")
			} else {
				out.WriteString(p + "\n")
			}
		}
		return system.Output{Stdout: []byte(out.String())}, nil
	}
	return exitWith(1, "Unknown command verb '%s'.", cmd.Args[0])
}

// The simulated systemd-creds seals with a host key kept where systemd
// keeps its own, and with the machine's TPM2 when asked, so a credential
// sealed on one simulated server doesn't open on another.
const (
	hostCredentialKey = "/var/lib/systemd/credential.secret"
	credentialMagic   = "racksail-simulated-credential\x00"
)

func (m *Machine) hasTPM2() bool { return m.facts.TPM && m.facts.TPMVersion == "2" }

// credentialCipher returns the cipher sealing credentials, creating the
// host key when create is set. The caller holds m.mu.
func (m *Machine) credentialCipher(withTPM, create bool) (cipher.AEAD, error) {
	n, ok := m.files[hostCredentialKey]
	if !ok {
		if !create {
			return nil, fmt.Errorf("the host key %s is missing", hostCredentialKey)
		}
		if _, ok := m.files["/var/lib/systemd"]; !ok {
			if err := m.mkdir("/var/lib/systemd", 0o755, system.Owner{}); err != nil {
				return nil, err
			}
		}
		secret := make([]byte, 4096)
		if _, err := rand.Read(secret); err != nil {
			return nil, err
		}
		if err := m.writeFile(hostCredentialKey, secret, fs.FileMode(0o400), system.Owner{}); err != nil {
			return nil, err
		}
		n = m.files[hostCredentialKey]
	}
	h := sha256.New()
	h.Write(n.data)
	if withTPM {
		h.Write([]byte("tpm2:" + m.facts.MachineID))
	}
	block, err := aes.NewCipher(h.Sum(nil))
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func systemdCreds(ctx context.Context, m *Machine, cmd system.Command) (system.Output, error) {
	if len(cmd.Args) == 0 {
		return exitWith(1, "systemd-creds needs a command in the simulator, such as encrypt, decrypt or has-tpm2.")
	}
	opts, operands := splitArgs(cmd.Args[1:])
	if err := m.lock(ctx); err != nil {
		return system.Output{}, err
	}
	defer m.mu.Unlock()
	tpm := m.hasTPM2()
	switch cmd.Args[0] {
	case "has-tpm2":
		if tpm {
			return system.Output{Stdout: []byte("yes\n+firmware\n+driver\n+system\n+subsystem\n+libraries\n")}, nil
		}
		return system.Output{Stdout: []byte("no\n-firmware\n-driver\n+system\n+subsystem\n+libraries\n"), ExitCode: 3}, nil
	case "encrypt":
		if len(operands) != 2 || operands[0] != "-" || operands[1] != "-" {
			return exitWith(1, "The simulator encrypts from standard input to standard output only.")
		}
		withKey := opts["with-key"]
		useTPM := false
		switch withKey {
		case "", "auto":
			useTPM = tpm
		case "host":
		case "tpm2", "host+tpm2":
			if !tpm {
				return exitWith(1, "TPM2 not supported on this system.")
			}
			useTPM = true
		default:
			return exitWith(1, "Unknown key type '%s'.", withKey)
		}
		if err := m.record("creds", "encrypt", opts["name"]); err != nil {
			return system.Output{}, err
		}
		aead, err := m.credentialCipher(useTPM, true)
		if err != nil {
			return exitWith(1, "Failed to set up the host key: %v", err)
		}
		nonce := make([]byte, aead.NonceSize())
		if _, err := rand.Read(nonce); err != nil {
			return system.Output{}, err
		}
		flag := byte(0)
		if useTPM {
			flag = 1
		}
		out := append([]byte(credentialMagic), flag)
		out = append(out, nonce...)
		out = aead.Seal(out, nonce, cmd.Stdin, []byte(opts["name"]))
		return system.Output{Stdout: out}, nil
	case "decrypt":
		if len(operands) != 2 || operands[0] != "-" || operands[1] != "-" {
			return exitWith(1, "The simulator decrypts from standard input to standard output only.")
		}
		rest, ok := bytes.CutPrefix(cmd.Stdin, []byte(credentialMagic))
		if !ok || len(rest) < 1 {
			return exitWith(1, "Failed to decrypt the credential: Bad message")
		}
		useTPM := rest[0] == 1
		if useTPM && !tpm {
			return exitWith(1, "Credential requires TPM2 support, but TPM2 support is not available.")
		}
		aead, err := m.credentialCipher(useTPM, false)
		if err != nil {
			return exitWith(1, "Failed to decrypt the credential: %v", err)
		}
		rest = rest[1:]
		if len(rest) < aead.NonceSize() {
			return exitWith(1, "Failed to decrypt the credential: Bad message")
		}
		plain, err := aead.Open(nil, rest[:aead.NonceSize()], rest[aead.NonceSize():], []byte(opts["name"]))
		if err != nil {
			return exitWith(1, "Failed to decrypt the credential: Bad message")
		}
		return system.Output{Stdout: plain}, nil
	}
	return exitWith(1, "Unknown command verb '%s'.", cmd.Args[0])
}
