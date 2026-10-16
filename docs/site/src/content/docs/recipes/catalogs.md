---
title: "Catalogs"
description: "Where recipes come from: the official catalog, catalogs of your own, how they are signed and checked, and mirrors."
---

A catalog is a signed index of recipes and the files they use. The panel offers the items of every catalog it trusts, and fetches nothing from any of them while the [marketplace module](../../modules/marketplace/) is off.

## The official catalog

Racksail's own catalog lives in [`catalog/`](https://github.com/vilanobeachflorida/racksail/tree/main/catalog) in the repository, where anyone can propose an item in a pull request. Its items are under the MIT license, so they can be reused anywhere. CI checks it on every change, and publishes it whenever it changes on `main` and once a day:

| File | What it is |
|---|---|
| `index.json` | Every item with its kind, version, license class, requirements and maintenance flags, and every file it has with the file's size and SHA-256 checksum |
| `index.json.sig` | The Ed25519 signature of `index.json`, 64 bytes |
| `catalog.tar.gz` | The index, its signature and every file it lists |

They are attached to the `catalog` release on GitHub and pushed to the GitHub Container Registry. The panel adds the official catalog the first time it starts, and trusts it through the public keys built into the binary, so a new key always arrives in a Racksail release of its own.

## How the panel checks a catalog

- The index must be signed by one of the keys the catalog was added with. An index that isn't is refused, and the panel keeps offering the last good one.
- An index built before the one the panel already has is refused, so nobody can serve an old index to bring back a version with a known problem.
- Every file the panel fetches, a recipe or an icon, must have the size and checksum the index lists.
- While the marketplace is on, the panel fetches every catalog's index once a day, and when you choose **Fetch now**.

## Maintenance flags

The index carries three lists from the catalog's `maintenance.yaml`:

- **featured** items are shown first;
- **broken** items, whose install test fails, can't be installed until they are fixed;
- **unmaintained** items are marked as such, and left out of the index after 180 days.

## Catalogs of your own

Anyone can publish a catalog: a company's internal apps, a hosting provider's stacks, a community's add-ons. A catalog is a directory laid out like `catalog/`, built and signed with the catalog tool:

```sh
go run ./catalog/tool keygen -private catalog.key -public catalog.pub
go run ./catalog/tool validate -dir my-catalog
go run ./catalog/tool index -dir my-catalog -name acme -key catalog.key -public catalog.pub -out public/ -archive public/catalog.tar.gz
go run ./catalog/tool verify -dir public/ -public catalog.pub
```

Serve `public/` from any web server. Keep `catalog.key` offline or in your CI's secrets; anyone who has it can publish items your panels will trust.

To add it to a panel, a server admin creates a catalog with its URL and the contents of `catalog.pub`:

```sh
curl -H "Authorization: Bearer $RACKSAIL_TOKEN" -X POST "https://panel.example.com:8443/api/v1/core/catalogs?dryRun=true" -d '{
  "spec": {"name": "acme", "title": "Acme apps", "url": "https://catalog.acme.example/",
           "keys": ["XcuPOUMHZ4ZlrTbiQJFYpqUi3eTHGSn+VZ6s0dFRPhc="]}
}'
```

Adding a catalog is a high-risk change, since it lets someone else offer software to install. A catalog can trust up to four keys, which lets you rotate yours: sign with the new key once every panel trusts both. Turning a catalog off (`disabled`) stops offering its items without forgetting it.

Every item is listed with the catalog it comes from and whether that is Racksail's own, and its installations record the catalog, the recipe's checksum and the index's.

## Mirrors

A catalog can list `mirrors`: other URLs serving the same three files and the files the index lists. The panel tries the catalog's URL first and then each mirror. Since everything is checked against the signed index, a mirror only needs to be reachable, not trusted.
