# Governance

How Racksail is run, how decisions are made and who makes them.

## Roles

- **Users** run Racksail and tell us what works and what doesn't.
- **Contributors** send issues, documentation, translations, recipes and code. Anyone can be one.
- **Maintainers** review and merge pull requests, triage issues, cut releases and look after the project's direction. They're listed below.
- **The lead maintainer** breaks ties and has the final say when maintainers can't agree.

## Maintainers

| Name | GitHub | Role |
|---|---|---|
| Sergio | [@vilanobeachflorida](https://github.com/vilanobeachflorida) | Lead maintainer |

To reach the maintainers privately about conduct, write to [conduct@racksail.com](mailto:conduct@racksail.com), which reaches the maintainers listed here and nobody else; the [code of conduct](CODE_OF_CONDUCT.md) says what happens next. Security problems go through [SECURITY.md](SECURITY.md) instead. Anything else is best raised in the open, in an issue or a discussion.

## Making decisions

- Day-to-day changes are decided in pull requests. A change is merged when a maintainer approves it and nobody with a stake in it objects.
- Settled technical choices are recorded in [docs/architecture/decisions.md](docs/architecture/decisions.md). Changing one takes a pull request that edits that file and explains why, and approval from a majority of maintainers.
- What ships in 1.0 is defined by [docs/architecture/features.md](docs/architecture/features.md), changed the same way.
- When maintainers disagree, they discuss it in the open, in the issue or pull request. If they still can't agree, the lead maintainer decides and writes down why.

## Becoming a maintainer

Contributors who have made sustained, high-quality contributions and reviews can be nominated by any maintainer. A nomination is accepted when a majority of maintainers agree and nobody objects within a week. Maintainers who are inactive for six months, or who ask to step down, become emeritus maintainers and can come back by the same process.

## Releases

Maintainers cut releases from the main branch. Releases are built in CI, signed and published on GitHub Releases with checksums and software bills of materials, as described in [decisions.md](docs/architecture/decisions.md).

## Licensing

The code is licensed under AGPL-3.0-or-later, catalog recipes under MIT and documentation under CC BY 4.0. Contributions are certified with the Developer Certificate of Origin rather than assigned to anyone, so no single person or company, maintainers included, can relicense the project.

## Name and logo

The Racksail name and logo identify the official project and its releases, and the maintainers look after them on the project's behalf.

- You can use the name to refer to Racksail, and the logo to link to it, in articles, talks, documentation, hosting offers and integrations.
- A modified version you distribute must use a different name and logo, so people can tell it apart from official releases. It may say it's based on Racksail.
- For anything else, ask the maintainers.

## Conduct

Everyone in the project follows the [code of conduct](CODE_OF_CONDUCT.md). The maintainers enforce it.

## Changing this document

Changes to this document are made by pull request and need approval from a majority of maintainers.
