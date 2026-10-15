# Catalog keys

`official.pub` is the public half of the Ed25519 key that signs the official catalog's index. Panels trust it for the official catalog, and the publishing job refuses to sign with any key that doesn't match it.

The private half lives only in the repository's `CATALOG_SIGNING_KEY` Actions secret, in the `catalog` environment, which only runs on `main`. Nobody keeps a copy outside it.

## Creating the key

A maintainer with admin rights on the repository does this once, on a machine they trust:

```sh
go run ./catalog/tool keygen -private catalog-signing.key -public catalog/keys/official.pub
gh secret set CATALOG_SIGNING_KEY --env catalog < catalog-signing.key
shred -u catalog-signing.key
```

Then commit `catalog/keys/official.pub` in a pull request of its own, so the change of trust is reviewed on its own.

## Rotating it

Rotate the key when someone who could read the secret leaves the project, or if it may have leaked.

1. Create a new key pair as above, but write the public key to `catalog/keys/next.pub`, and set the secret to the new private key only after the next step has shipped.
2. Release a Racksail version that trusts both keys, so panels accept indexes signed with either.
3. Replace `official.pub` with `next.pub`, update the secret, and publish the catalog again.
4. Once that release has been out for a full support period, release a version that only trusts the new key.

If the key leaked, publish a security advisory and skip the waiting: panels that don't update will stop accepting new indexes rather than accept forged ones.
