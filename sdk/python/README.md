# Python SDK

This directory is the add-on SDK for Python, the `racksail-addon` package. It follows the add-on protocol in [spec/addon-protocol.json](../../spec/addon-protocol.json) and uses nothing but the standard library, so add-ons install with few dependencies. It needs Python 3.11 or later; Debian 13 has 3.13.

```python
import racksail_addon

if not racksail_addon.supports("v1"):
    raise SystemExit("this SDK doesn't speak the hook API version the add-on was written for")
```

`API_VERSIONS` lists the hook API versions the SDK speaks, newest first, and `supports` says whether it speaks one; a test keeps the newest equal to the protocol document's version. That is all the SDK has: an add-on serves the protocol itself, as [the SDK overview](../README.md) describes.

```sh
cd sdk/python
PYTHONPATH=src python3 -m unittest discover -s tests
```
