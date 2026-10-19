---
title: "Governance"
description: "How decisions are made and who maintains Racksail."
---

Racksail is run in the open, by its maintainers, under rules anyone can read. The rules themselves are in [GOVERNANCE.md](https://github.com/vilanobeachflorida/racksail/blob/main/GOVERNANCE.md), with the list of current maintainers; this page explains how they work.

## Who does what

| Role | Who | What they do |
|---|---|---|
| Users | Anyone running Racksail | Tell the project what works and what doesn't |
| Contributors | Anyone who sends something | Issues, documentation, translations, recipes and code |
| Maintainers | Listed in GOVERNANCE.md | Review and merge pull requests, triage issues, cut releases and look after the project's direction |
| Lead maintainer | One of the maintainers | Breaks ties when maintainers can't agree |

## How decisions are made

- **Everyday changes** are decided in pull requests. A pull request is merged when a maintainer approves it and nobody with a stake in it objects.
- **Settled technical choices** are recorded in [decisions.md](https://github.com/vilanobeachflorida/racksail/blob/main/docs/architecture/decisions.md): the platform, the software Racksail manages, defaults, paths and ports. To change one, open a pull request that edits that file and explains why. It needs the approval of a majority of maintainers.
- **What 1.0 includes** is [features.md](https://github.com/vilanobeachflorida/racksail/blob/main/docs/architecture/features.md), changed the same way.
- **Disagreements** are discussed in the open, in the issue or pull request. If the maintainers still can't agree, the lead maintainer decides and writes down why.

## Becoming a maintainer

Contributors who have made sustained, careful contributions and reviews can be nominated by any maintainer. The nomination is accepted when a majority of maintainers agree and nobody objects within a week.

Maintainers who have been inactive for six months, or ask to step down, become emeritus maintainers. They can come back the same way they joined.

## Releases

Maintainers cut releases from `main`. Every release is built in CI, signed with Sigstore and published on GitHub Releases with its checksums and software bills of materials, never built on someone's laptop; [Updates](../../install/updates/) says how servers find and check them.

## Licensing

The code is licensed under AGPL-3.0-or-later, the official catalog's recipes under MIT and the documentation under CC BY 4.0. Contributors certify the [Developer Certificate of Origin](../contributing/#commits) by signing off their commits instead of assigning their work to anyone, so no single person or company, the maintainers included, can relicense the project.

## The name and logo

The Racksail name and logo tell people what the official project and its releases are, and the maintainers look after them for the project.

- Use the name to refer to Racksail, and the logo to link to it, in articles, talks, documentation, hosting offers and integrations.
- A modified version you distribute needs a name and logo of its own, so people can tell it from official releases. It may say it's based on Racksail.
- For anything else, ask the maintainers.

The [brand pages](../../brand/logo/) show how to use the logo.

## Conduct

Everyone in the project follows the [code of conduct](../code-of-conduct/), and the maintainers enforce it. Reports go to [conduct@racksail.com](mailto:conduct@racksail.com), which reaches the maintainers and nobody else.

## Changing the rules

GOVERNANCE.md changes by pull request, with the approval of a majority of maintainers.
