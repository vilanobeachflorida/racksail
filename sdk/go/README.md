# Go SDK

This directory is the add-on SDK for Go. It is a Go module of its own, `github.com/vilanobeachflorida/racksail/sdk/go`, so add-ons can import it without the rest of Racksail, and it defines its own types for the add-on protocol in [spec/addon-protocol.json](../../spec/addon-protocol.json) rather than reusing the panel's internal packages. It depends on nothing but the standard library.

```go
import addon "github.com/vilanobeachflorida/racksail/sdk/go"

if !addon.Supports("v1") {
	// This SDK doesn't speak the hook API version the add-on was written for.
}
```

`addon.APIVersions` lists the hook API versions the SDK speaks, newest first, and `addon.Supports` says whether it speaks one; a test keeps the newest equal to the protocol document's version. That is all the SDK has: an add-on serves the protocol itself, as [the SDK overview](../README.md) describes, and the official add-ons in [addons/](../../addons/) show how in Go.

```sh
cd sdk/go
go test ./...
```

Versions of this module are tagged `sdk/go/vX.Y.Z`, as Go expects for a module in a subdirectory.
