---
title: "Translations"
description: "Translate the panel into your language: where its words live, how a language is added, and how translations are checked."
---

Every word the panel shows comes from a message catalog, so translating it means writing catalogs, never changing code. English is the panel's own language, and every other language goes on top of it: a word that isn't translated yet shows in English rather than as an ID.

## Where the words live

The catalogs are JSON files in [ui/](https://github.com/vilanobeachflorida/racksail/tree/main/ui), one per part of the panel, each loaded with the screens that need it:

| Catalog | Holds |
|---|---|
| `ui/src/app/messages/en.json` | The shell: navigation, the command bar, the home screen and messages for every error code of the core |
| `ui/src/app/screens/messages/en.json` | The panel's own screens: resources, jobs, events, changes, search and settings |
| `ui/src/app/screens/inbox/messages/en.json` | The attention inbox |
| `ui/src/app/screens/kinds/messages/en.json` | The names and fields of the core's kinds of resources |
| `ui/src/app/identity/messages/en.json` | Signing in, your account, people and access |
| `ui/src/modules/<id>/messages/en.json` | Each module's screens, and its error codes |

Messages are written in the ICU message format that react-intl reads. A message names its placeholders, and says how a sentence changes with a number or a choice:

```json
{
  "web.sites.count": "{count, plural, one {# site} other {# sites}}",
  "web.site.deployed": "{site} was deployed by {name}.",
  "error.web.domain_taken": "{value} is already used by another site."
}
```

IDs start with the module's id, or with `error.` and the code for the messages of error codes. The API answers errors with a stable code, and the panel shows its own words for it, so errors are translated too.

## Add a language

1. Next to each `en.json`, add a catalog named after the language's tag, such as `de.json`, `fr.json` or `pt-BR.json`, with the same IDs.
2. Translate the values. Keep every placeholder, such as `{site}`, and the structure of plurals and choices; the words around them are yours to order as your language needs.
3. Run `make dev`, choose the language and go through the screens you translated. A message the panel can't read shows its ID instead, and the browser's console says which one it is.

The language is offered once the shell's catalog, `ui/src/app/messages/<tag>.json`, exists: under **Settings → General**, where it's kept in that browser, and in your profile, which keeps it for you. Until someone chooses one, the panel follows the browser's languages, first an exact match, then the same language, then English. Languages written right to left, such as Arabic, Hebrew, Persian and Urdu, turn the whole layout around, and dates, times, numbers and sizes follow each language's conventions.

Write the way the panel writes: plain words, sentence case and whole sentences in one message, so they can be reordered. [Voice](../../brand/voice/) has the guidelines.

## How translations are checked

Two pseudo-locales show what a translation will go through, without one:

- **en-XA** accents every letter and makes text about 40% longer, which shows text that didn't come from a catalog and layouts that break with longer words;
- **ar-XB** writes every word backwards, right to left, which shows layouts that don't turn around.

In development they can be chosen like any language. `make e2e-pseudo` opens every screen in both on a desktop and a phone, and fails on text that skipped the catalogs, text that doesn't fit and layouts that don't mirror. The UI's tests run the shell's screens in en-XA too.

## What stays in English

The racksail command, the installer, the console and the emails Racksail sends speak English. The API's error messages are English too, but people never need to read them in the panel, which has its own words for every code.

## Send a translation

Send new and corrected catalogs as a pull request, as described in [Contributing](../contributing/), with one language per pull request. Someone who reads the language reviews it before it's merged.
