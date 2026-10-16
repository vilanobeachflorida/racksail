# Python SDK

This directory is the add-on SDK for Python, the `racksail-addon` package. It serves the add-on protocol in [spec/addon-protocol.json](../../spec/addon-protocol.json) with nothing but the standard library, so add-ons install with few dependencies. It needs Python 3.11 or later; Debian 13 has 3.13.

```python
import racksail_addon

if not racksail_addon.supports("v1"):
    raise SystemExit("this SDK doesn't speak the hook API version the add-on was written for")
```

`API_VERSIONS` lists the hook API versions the SDK speaks, newest first; a test keeps the newest equal to the protocol document's version. Serving the protocol, the hook types, the API client, reading `addon.yaml` and the local test harness build on it, as [the SDK overview](../README.md) describes.

```sh
cd sdk/python
PYTHONPATH=src python3 -m unittest discover -s tests
```
