# Runtimes

Language runtimes sites run on: PHP versions with their extensions, Node.js lines, Python, Ruby and others. A runtime's recipe creates the build recipe the builder module turns into an immutable, versioned directory under `/opt/racksail/runtimes/`, which sites then mount read-only. Popular builds are published prebuilt and signed, so most servers download them instead of compiling.

Rules for runtimes, besides the catalog's:

- The version is the exact upstream version, such as `8.4.13`. Patch releases replace the previous recipe; each major or minor line is its own item, such as `php-8-4`, so sites stay on the line they chose.
- Downloads are the upstream source or binary archives, pinned by checksum and size and verified against the upstream signature where there is one.
- A `resource` check of the build, and an install test, which builds or downloads the runtime.
- End-of-life lines stay available for the sites that need them, with `description` saying so; sites using them get the strictest sandbox and a warning in the panel.

```
runtimes/php-8-4/
  recipe.yaml
  icon.svg
```
