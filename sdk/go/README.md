# Go SDK

This directory is the add-on SDK for Go. It is a Go module of its own, `github.com/vilanobeachflorida/racksail/sdk/go`, so add-ons can import it without the rest of Racksail, and it defines its own types for the add-on protocol in [spec/addon-protocol.json](../../spec/addon-protocol.json) rather than reusing the panel's internal packages. It depends on nothing but the standard library.

```go
import addon "github.com/vilanobeachflorida/racksail/sdk/go"

if !addon.Supports("v1") {
	// This SDK doesn't speak the hook API version the add-on was written for.
}
```

`addon.APIVersions` lists the hook API versions the SDK speaks, newest first; a test keeps the newest equal to the protocol document's version. Serving the protocol, the hook types, the API client, reading `addon.yaml` and the local test harness build on it, as [the SDK overview](../README.md) describes.

```sh
cd sdk/go
go test ./...
```

Versions of this module are tagged `sdk/go/vX.Y.Z`, as Go expects for a module in a subdirectory.
