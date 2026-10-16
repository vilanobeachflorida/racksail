---
title: "UI extensions"
description: "Add navigation entries, tabs, home cards, settings pages, command-bar actions and table columns."
---

An add-on adds to the panel by declaring entries under `ui` in its `addon.yaml`. It never ships code that runs in the panel: each entry names a place in the panel and a page or route the add-on serves itself.

```yaml
ui:
  - {type: nav, label: Site guard, icon: shield, path: /overview}
  - {type: tab, label: Guard, kind: web.site, path: /site}
  - {type: card, label: Blocked today, path: /card}
  - {type: command, label: Review blocked visitors, path: /review}
  - {type: settings, label: Guard rules, path: /rules}
  - {type: column, label: Blocked, kind: web.site, path: /blocked}
routes:
  - {method: GET, path: /blocked, summary: Visitors blocked per site}
```

| Type | Where it shows | What `path` is |
|---|---|---|
| `nav` | An entry in the navigation, which opens the page | A page |
| `tab` | A tab on the screen of every resource of `kind` | A page, asked for with `?resource=<id>` of the resource on screen |
| `card` | A card on the home screen | A page |
| `command` | An action in the command bar, which opens the page | A page |
| `settings` | A page under **Settings → Add-ons** | A page |
| `column` | A column in the list of `kind`'s resources, after the kind's own | One of the add-on's `GET` routes, which answers the column's values |

`label` is what the panel calls the entry. `icon` names one of these icons from [Lucide](https://lucide.dev/icons/): `activity`, `bell`, `chart-line`, `cloud`, `database`, `file-text`, `globe`, `hard-drive`, `key-round`, `lock`, `mail`, `puzzle`, `server`, `shield`, `shield-check`, `users` and `wrench`; an entry without one, or with another name, shows the add-on icon.

An add-on whose tab or column names no kind, or whose column's path isn't one of its `GET` routes, is refused when it is installed rather than left out of the panel.

## Who sees them

`GET /api/v1/addons/entries` lists the entries of the running add-ons the person may use, which the panel shows: everyone signed in may use a server's add-ons, and an account's add-ons those who may see that account's add-ons. An add-on that isn't running, such as one that is off or in safe mode, has no entries.

## Pages

The navigation, commands and settings pages open the add-on's page in the panel, at `/addons/<id>/<path>`; tabs and cards show theirs in place. The panel loads a page from `/api/v1/addons/<id>/ui/<path>`, which it passes on to the add-on's `GET /ui/<path>` with the query, and shows the answer in a frame:

- The frame is sandboxed without the panel's origin, so the page can't read the panel's cookies, its session or its other pages. The add-on isn't told who is looking at the page.
- The answer carries the panel's policy for add-on pages, which allows inline styles and `data:` images, keeps the page from loading anything from other sites, and lets no one but the panel frame it.
- Since the frame has no session of the panel's, requests the page makes from inside it, for scripts, styles, images or the add-on's routes, aren't signed in and are refused, and inline scripts don't run. A page has to be complete as the add-on sends it: the HTML of what the person should see, with its styles inline and its images as `data:` URLs.

A card's frame is 12rem tall and a tab's 24rem, both as wide as the screen allows; the pages the navigation, commands and settings open fill the screen.

## Columns

For a column, the panel asks the add-on's route for the values of the resources in the list, at most 100 at a time:

```http
GET /api/v1/addons/site-guard/blocked?ids=sit_01jq…,sit_01jr…
```

The add-on gets it as `GET /routes/blocked?ids=…`, as every request to its routes, with who is asking in `Racksail-Actor`. It answers with the text to show for each resource it knows about:

```json
{"values": {"sit_01jq…": "12 visitors", "sit_01jr…": "none"}}
```

Values that aren't text, and resources it leaves out, show as empty cells. People can sort and filter the list by the column like by any other, and hide it.
