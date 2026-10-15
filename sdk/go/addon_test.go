package addon

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The SDK speaks the version of the add-on protocol the panel publishes.
func TestAPIVersionMatchesTheProtocol(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "spec", "addon-protocol.json"))
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Info struct {
			Version string `json:"version"`
		} `json:"info"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	if len(APIVersions) == 0 || APIVersions[0] != doc.Info.Version {
		t.Errorf("the SDK's newest version is %v, the protocol's %q", APIVersions, doc.Info.Version)
	}
	if !Supports(doc.Info.Version) || Supports("v0") {
		t.Error("Supports")
	}
}
