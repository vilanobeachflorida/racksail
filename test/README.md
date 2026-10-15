# End-to-end tests

This directory is for tests that need a whole Racksail: running against the simulated server, or installed on a Debian 13 machine. Unit tests, golden files and simulated flows live next to the code they test and run with `make check`.

## Layout

| Path | For |
|---|---|
| `e2e/` | Playwright tests of the panel, run against `racksail dev` |
| `qemu/` | The harness that boots the installer ISO in QEMU, installs unattended, reboots and runs the API and UI suites |
| `fixtures/` | Shared test data: answers files, synthetic servers to migrate from (CWP, cPanel, DirectAdmin, HestiaCP, Plesk, Unraid), zone files, backup archives and recipes |

## Running against racksail dev

Give the instance under test its own data directory and addresses, so it can run next to a development instance:

```sh
racksail dev --addr 127.0.0.1:7170 --edge-http 127.0.0.1:0 --edge-https 127.0.0.1:0 --data .dev-e2e
```

The QEMU harness needs Linux with KVM. It is meant to run in CI every night and before every release (features.md, section 37).

## Rules

- Fixtures are synthetic. Nothing here comes from a real server, customer or mailbox.
- Every migration source has fixtures, and every parser that reads outside input has a fuzz test next to its code.
- Tests clean up after themselves and don't depend on the order they run in.
