# Services

Shared services sites and apps use: database engines such as MariaDB, MySQL, Percona Server and PostgreSQL, and caches such as Valkey and Memcached. A service's recipe creates the server resource (`databases.server` for the engines) with its settings; the module installs the software from the pinned upstream source the first time something needs it.

Rules for services, besides the catalog's:

- A health check of the service itself, usually a `unit` or `tcp` check.
- An install test.
- `upgrade.strategy` is `snapshot-then-replace` for anything that keeps data, and `uninstall.keepData` is `true`.

```
services/postgresql-17/
  recipe.yaml
  icon.svg
```
