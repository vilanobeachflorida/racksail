# Add-ons

Add-ons run as their own sandboxed process and extend the panel through hooks, providers, API routes and declarative screens: an LDAP or OIDC sign-in, a DNS provider, a billing system. An add-on item holds its manifest, `addon.yaml`, next to a recipe that carries what every catalog item has: maintainers, license class, icon and categories. The recipe has no inputs or resources; the marketplace installs the add-on from its manifest, after the person installing it has approved its permissions.

Rules for add-ons, besides the catalog's:

- `addon.yaml` passes [spec/schemas/addon.schema.json](../../spec/schemas/addon.schema.json) and the panel's own checks: every hook has the permission for its level, and the add-on supports the current hook API version.
- Its id, version and license are the recipe's.
- The add-on's program is built in CI from `source` and downloaded pinned by checksum. Racksail's releases publish the official add-ons' archives, which `make addons` builds and `make release` puts with the release's other files, checksummed and signed with them, as `<id>-<version>-linux-<arch>.tar.gz`. Once a release has an add-on's version, its recipe lists the archive for each architecture under `downloads`, and `go run ./catalog/tool pin -write catalog/addons/<id>` fills in their checksums and sizes.
- Permissions are as narrow as the add-on's job: the kinds and fields it reads, the hosts it reaches, and `secrets` only when it can't work without them.
- A health check of the add-on's process, a `unit` check of `racksail-addon@<id>.service`, and an install test, which installs the add-on with the permissions it asks for.

```
addons/oidc-sign-in/
  recipe.yaml
  addon.yaml
  icon.svg
```

The LDAP and OpenID Connect sign-in add-ons are built from [addons/](../../addons/) in this repository, whose README says how they are built and tested.
