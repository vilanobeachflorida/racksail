package installer

import (
	"encoding/json"
	"path"

	"github.com/vilanobeachflorida/racksail/internal/agent"
	"github.com/vilanobeachflorida/racksail/internal/system"
)

// BootstrapFile is where the installer leaves the admin's account for the
// panel, in its state directory. The panel makes it its first admin on its
// first start and removes the file.
const BootstrapFile = "bootstrap.json"

// handover is what the panel reads from BootstrapFile.
type handover struct {
	Username     string   `json:"username"`
	PasswordHash string   `json:"passwordHash,omitempty"`
	SSHKeys      []string `json:"sshKeys,omitempty"`
}

// AdminHandover returns the step that leaves the answers' admin account for
// the panel: the username with the password hash and SSH keys, never a
// plain password. ok is false when the answers name no admin.
func AdminHandover(a *Answers) (step agent.Step, ok bool, err error) {
	acct := a.Account
	if acct.Username == "" || (acct.PasswordHash == "" && len(acct.SSHKeys) == 0) {
		return agent.Step{}, false, nil
	}
	data, err := json.Marshal(handover{Username: acct.Username, PasswordHash: acct.PasswordHash, SSHKeys: acct.SSHKeys})
	if err != nil {
		return agent.Step{}, false, err
	}
	return agent.WriteFile.Step(agent.WriteFileInput{
		Path: path.Join(system.StateDir, BootstrapFile), Content: data, Mode: 0o600,
		Owner: system.Owner{User: "racksail", Group: "racksail"},
	}), true, nil
}
