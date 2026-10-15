# Go SDK

This directory is for the add-on SDK for Go. It is a Go module of its own, so add-ons can import it without the rest of Racksail, and defines its own types for the add-on protocol in [spec/addon-protocol.json](../../spec/addon-protocol.json) rather than reusing the panel's internal packages.

See [the SDK overview](../README.md) for what every SDK provides.
