"""The Python SDK for Racksail add-ons.

An add-on runs as its own sandboxed systemd service and talks to the panel
over a Unix socket: the panel calls the add-on's hooks, and the add-on calls
the panel's API with a token limited to the permissions it was granted. The
protocol is spec/addon-protocol.json in the Racksail repository. The SDK
uses nothing but the standard library, so add-ons install with few
dependencies.
"""

__all__ = ["API_VERSIONS", "supports"]

#: The hook API versions this SDK speaks, newest first.
API_VERSIONS = ("v1",)


def supports(version: str) -> bool:
    """Report whether the SDK speaks a hook API version."""
    return version in API_VERSIONS
