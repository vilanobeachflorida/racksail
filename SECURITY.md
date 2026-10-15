# Security policy

Racksail runs as root on servers connected to the internet and holds other people's websites, email and files, so we take reports seriously and answer quickly.

## Reporting a vulnerability

Report it privately through GitHub: on the repository's **Security** tab, choose **Report a vulnerability**, or go straight to [the private reporting form](https://github.com/vilanobeachflorida/racksail/security/advisories/new). Please don't open a public issue, pull request or discussion about it.

A useful report says:

- what the problem is and what an attacker could do with it
- the version (`racksail version`) or commit you tested
- how to reproduce it, ideally with the smallest steps or a proof of concept
- whether it's already public or known to others

## What happens next

| Step | Target |
|---|---|
| We acknowledge your report | within 3 working days |
| We confirm the problem, or explain why it isn't one | within 10 working days |
| A fix is released for a critical or high severity problem | within 30 days of confirmation |
| A fix is released for a medium or low severity problem | within 90 days of confirmation |

We keep you informed along the way, agree on a disclosure date with you, and publish a GitHub security advisory with a CVE when the fix is out. We credit you in the advisory unless you'd rather we didn't.

## Scope

In scope:

- the `racksail` binary in every role: panel, agent, edge, installer, command line and `racksail dev`
- the web UI and the API
- the ISO, the install script, cloud images and the update mechanism
- configuration Racksail generates for the software it manages, such as web server, mail and firewall rules
- recipes, themes and add-ons in the official catalog in this repository

Out of scope:

- vulnerabilities in upstream software Racksail installs, such as PHP, Postfix or MariaDB, which belong with that project; do tell us if Racksail's configuration makes one worse
- third-party catalogs and add-ons, which belong with their authors
- attacks that need physical access to the server or an already compromised admin account
- denial of service by volume, social engineering, and reports from automated scanners without a demonstrated impact

## Supported versions

There is no release yet. Once there is, security fixes go to the latest stable release and to the beta and nightly channels.

## Safe harbor

We won't pursue or support legal action against research done in good faith under this policy: testing only on systems you own or have permission to test, not accessing or keeping other people's data beyond what's needed to show the problem, and giving us reasonable time to fix it before disclosure.
