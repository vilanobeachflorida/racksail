---
title: "UI extensions"
description: "Add navigation entries, tabs, home cards, settings pages and command-bar actions, screens of your own and API routes."
---

An add-on can add to the panel in three ways: declarative entries the panel draws itself, screens the add-on serves for anything the entries can't express, and API routes. All three are declared in [addon.yaml](../manifest/), so the people who approve the add-on see what it adds.

## Entries

Each entry under `ui` is one addition to the panel:

```yaml
ui:
  - type: nav
    label: Site guard
    icon: shield
    path: overview.html
  - type: tab
    label: Security
    icon: shield
    kind: web.site
    path: site-tab.html
  - type: column
    label: Findings
    kind: web.site
```

| Type | Where it goes |
|---|---|
| `nav` | An entry in the navigation |
| `tab` | A tab on the detail screen of every resource of `kind` |
| `card` | A card on the home screen |
| `command` | An action in the command bar |
| `settings` | A page in the settings |
| `column` | A column in the list of resources of `kind` |

| Field | What it holds |
|---|---|
| `type` | One of the types above |
| `label` | What the entry says, in the add-on's own words |
| `icon` | The name of an icon from the [Lucide](https://lucide.dev/icons/) set the panel uses, such as `shield` |
| `kind` | For tabs and columns, the kind of resource they belong to, such as `web.site` |
| `path` | The screen the entry opens, which the add-on serves at `/ui/<path>` |

The runtime attaches the entries while the add-on is on, and takes them away when it's turned off, removed or stopped by safe mode, so they come and go with it. `GET /api/v1/addons/entries` lists the entries of every add-on the person asking may use, with the add-on each belongs to; `?point=ui.tab` narrows it to one type. Everyone signed in may use an add-on a server admin installed, and an account's add-on is for those who may see that account's add-ons.

## Screens

A screen is a page the add-on serves itself at `GET /ui/<path>` on its socket. The panel shows it at `/api/v1/addons/<id>/ui/<path>`, inside a frame, under a Content Security Policy that keeps it apart from the panel:

- it runs in a sandbox of its own origin, so it can never read the panel's session, cookies or pages;
- it may run its own scripts and styles, load images, fonts and data from its own address, and submit forms there, and nothing from anywhere else;
- it can't be framed by any other site, and sends no referrer.

The request reaches the add-on with `Racksail-Actor`, who is looking, as JSON with their `type`, `id`, `name`, `user` and `source`, and `Racksail-Request-Id`, the panel's ID for the request, for the add-on's logs. The person's cookies and credentials stay with the panel. A screen gets its data from the add-on's own API routes, below.

## API routes

`routes` declares the API operations the add-on serves:

```yaml
routes:
  - method: GET
    path: /sites/{id}/findings
    summary: List what the guard found on a site
  - method: POST
    path: /sites/{id}/scan
    summary: Scan a site now
```

The panel serves them under `/api/v1/addons/<id>/`, here `GET /api/v1/addons/site-guard/sites/{id}/findings`, where `{name}` stands for one part of the path. A request is passed on to the add-on, at `/routes/<path>` on its socket, only when:

- the person is signed in, by a session or an API token, and may use the add-on, as for entries;
- the method and the path match a route the manifest declares. Anything else answers `404` without reaching the add-on.

As with screens, the add-on learns who asks from `Racksail-Actor` and never sees their credentials. It decides itself what that person may do with what it serves, and changes it makes on their behalf go through its own API access, with its own [permissions](../permissions/). Bodies, both ways, are limited to 8 MiB, and an add-on that doesn't answer gives `addons.unreachable`.

## Words and look

Labels are the add-on's own words, shown as they are written, so write them the way the panel writes: sentence case and plain words. Screens follow the panel's look best when they use the [brand's patterns](../../brand/ui-patterns/); themes can't reach into an add-on's frame, and an add-on's screen can't restyle the panel either.
