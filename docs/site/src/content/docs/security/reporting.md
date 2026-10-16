---
title: "Reporting a vulnerability"
description: "How to report a security problem privately, and what happens next."
---

Racksail runs as root on servers connected to the internet and holds other people's websites, email and files, so security reports come first. Report a problem privately, and never in a public issue, pull request or discussion. The full policy is [SECURITY.md](https://github.com/vilanobeachflorida/racksail/blob/main/SECURITY.md) in the repository.

## Report it

Use GitHub's private reporting: on the repository's **Security** tab, choose **Report a vulnerability**, or go straight to [the reporting form](https://github.com/vilanobeachflorida/racksail/security/advisories/new). Only the maintainers see what you write there.

A useful report says:

- what the problem is, and what an attacker could do with it
- the version you tested, from `racksail version`, or the commit
- how to reproduce it, ideally in the fewest steps or with a proof of concept
- whether it is public already, or known to anyone else

## What happens next

| Step | When |
|---|---|
| The maintainers acknowledge your report | Within 3 working days |
| They confirm the problem, or explain why it isn't one | Within 10 working days |
| A fix is released for a critical or high-severity problem | Within 30 days of confirming it |
| A fix is released for a medium or low-severity problem | Within 90 days of confirming it |

You hear how it's going along the way, and agree on a disclosure date with the maintainers. When the fix is out, they publish a GitHub security advisory with a CVE, crediting you unless you'd rather they didn't.

## What's in scope

- the `racksail` binary in every role: the panel, the agent, the edge, the installer, the command line and `racksail dev`
- the web UI and the API
- the ISO, the install script, cloud images and the way Racksail updates itself
- configuration Racksail generates for the software it manages, such as web server, mail and firewall settings
- recipes, themes and add-ons in the official catalog

These belong elsewhere:

- problems in software Racksail installs, such as PHP, Postfix or MariaDB, which go to that project; do tell the maintainers when Racksail's configuration makes one worse
- third-party catalogs and add-ons, which go to their authors
- attacks that need physical access to the server or an admin account that is already compromised
- denial of service by sheer volume, social engineering, and reports from automated scanners without a demonstrated impact

## Supported versions

There is no release yet. Once there is, security fixes go to the latest stable release and to the beta and nightly channels, which servers receive as described in [Updates](../../install/updates/).

## Safe harbor

The project won't pursue or support legal action against research done in good faith under this policy: testing only on systems you own or may test, not looking at or keeping other people's data beyond what it takes to show the problem, and giving the maintainers reasonable time to fix it before you disclose it.

For problems with how people behave in the project, rather than with the software, see the [code of conduct](../../community/code-of-conduct/).
