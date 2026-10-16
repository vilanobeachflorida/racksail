---
title: "UI extensions"
description: "Add navigation entries, tabs, home cards, settings pages, command-bar actions and table columns, screens of your own and API routes."
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
    path: /sites/findings
routes:
  - method: GET
    path: /sites/findings
    summary: Count what the guard found on each site
```

| Type | Where it goes | What it shows |
|---|---|---|
| `nav` | An entry in the navigation | The screen at `path`, which it opens in the panel at `/addons/<id>/<path>` |
| `tab` | A tab on the detail screen of every resource of `kind` | The screen at `path`, asked for with `?resource=<id>` of the resource on screen |
| `card` | A card on the home screen | The screen at `path`, in the card |
| `command` | An action in the command bar | The screen at `path`, opened as a navigation entry opens it |
| `settings` | A page under **Settings → Add-ons** | The screen at `path`, opened as a navigation entry opens it |
| `column` | A column in the list of resources of `kind`, after the kind's own | The values one of the add-on's `GET` routes answers, [below](#columns) |

| Field | What it holds |
|---|---|
| `type` | One of the types above |
| `label` | What the entry says, in the add-on's own words |
| `icon` | The name of an icon from the [Lucide](https://lucide.dev/icons/) set the panel uses: `activity`, `bell`, `chart-line`, `cloud`, `database`, `file-text`, `globe`, `hard-drive`, `key-round`, `lock`, `mail`, `puzzle`, `server`, `shield`, `shield-check`, `users` or `wrench`. Without one, or with another name, the entry has the add-on icon |
| `kind` | For tabs and columns, the kind of resource they belong to, such as `web.site` |
| `path` | The screen the entry shows, which the add-on serves at `/ui/<path>`; for a column, the path of one of its `GET` routes |

A manifest with a tab or a column that names no `kind`, or a column whose `path` isn't one of its `GET` routes, is refused, so every entry an add-on declares has somewhere to show.

The runtime attaches the entries while the add-on is on, and takes them away when it's turned off, removed or stopped by safe mode, so they come and go with it. `GET /api/v1/addons/entries` lists the entries of every add-on the person asking may use, with the add-on each belongs to; `?point=ui.tab` narrows it to one type. Everyone signed in may use an add-on a server admin installed, and an account's add-on is for those who may see that account's add-ons.

### Columns

For a column, the panel asks the add-on's route for the values of the resources in the list, a hundred at a time, with their IDs in `ids`:

```http
GET /api/v1/addons/site-guard/sites/findings?ids=sit_01jq…,sit_01jr…
```

The add-on answers with the text to show for each resource it knows about:

```json
{"values": {"sit_01jq…": "3 findings", "sit_01jr…": "none"}}
```

Values that aren't text, and resources the answer leaves out, show as empty cells. People sort, filter and hide the column as they do the kind's own.

## Screens

A screen is a page the add-on serves itself at `GET /ui/<path>` on its socket, along with what the page loads: scripts, style sheets, images and fonts. When someone opens one, at `/api/v1/addons/<id>/ui/<path>`, the panel checks they may use the add-on and sends the frame it shows the screen in on to an address of its own, `/api/v1/addons/<id>/frames/<token>/ui/<path>`, with the query the screen was asked with. The token in it stands for that person with that add-on and nothing else, so what the page loads and asks for under the frame's address is signed in as them, without the panel's session, and counts against their own rate limit:

- the page runs in a sandbox with an origin of its own, so it can never read the panel's session, cookies or pages;
- its scripts run when the add-on serves them, as `<script src="app.js">`, or inline with the nonce its request brings in `Racksail-Nonce`, as `<script nonce="…">`; styles may be inline too, and images and fonts `data:` URLs;
- its scripts can call the routes the add-on declares at `routes/<path>` under the frame's address, which the request brings in `Racksail-Frame-Base`, such as `/api/v1/addons/site-guard/frames/<token>/`: a page at `ui/overview.html` reaches `GET /sites/{id}/findings` at `../routes/sites/s1/findings`;
- it can't load anything from other sites or reach anything else of the panel's, it can't be framed by other sites, and it sends no referrer.

A frame's address works while the screen is in use, for half an hour after its last request and twelve hours at most, and stops when the add-on is turned off or stops, once the person who opened it is turned off or may no longer use the add-on, and once what they opened it with stops working: when they sign out or their session is ended, or the API token they used is revoked or expires. Opening the screen again gives it a new one. A person keeps 16 frames open at most, and opening another closes their own used longest ago, never someone else's. A card's frame is 12rem tall and a tab's 24rem, and a screen that an entry opens fills the panel's screen.

The request reaches the add-on with `Racksail-Actor`, who is looking, as JSON with their `type`, `id`, `name`, `user` and `source`, and `Racksail-Request-Id`, the panel's ID for the request, for the add-on's logs. The person's cookies and credentials stay with the panel. Of the headers the add-on answers with, the panel keeps only those of the content's type, language and caching, so the frame's Content Security Policy is always the panel's.

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

- the person is signed in, by a session or an API token, and may use the add-on, as for entries, or it comes from the frame of one of the add-on's screens, as the person who opened it;
- the method and the path match a route the manifest declares. Anything else answers `404` without reaching the add-on.

As with screens, the add-on learns who asks from `Racksail-Actor` and never sees their credentials. It decides itself what that person may do with what it serves, and changes it makes on their behalf go through its own API access, with its own [permissions](../permissions/). Bodies, both ways, are limited to 8 MiB, and an add-on that doesn't answer gives `addons.unreachable`.

## Words and look

Labels are the add-on's own words, shown as they are written, so write them the way the panel writes: sentence case and plain words. Screens follow the panel's look best when they use the [brand's patterns](../../brand/ui-patterns/); themes can't reach into an add-on's frame, and an add-on's screen can't restyle the panel either.
