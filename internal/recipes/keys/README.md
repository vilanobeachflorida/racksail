# Official catalog keys

The public keys panels trust for the official catalog, built into the binary. They are copies of the `.pub` files in [catalog/keys](../../../catalog/keys/), and a test fails when the two differ, so trusting a new key always ships in a release of its own.

While a key is rotated, both the current key and the next one are here, as [catalog/keys/README.md](../../../catalog/keys/README.md) describes.
