---
title: "UI patterns"
description: "The patterns screens share: previews, empty states, confirmations and toasts."
---

Every screen in the panel is built from the same parts, so something learned once works everywhere. Modules and add-ons use these patterns rather than inventing their own. All of them take their colors, sizes and motion from theme tokens, work with the keyboard alone and meet WCAG 2.2 AA.

## The frame

- **Icon rail** on the start side: Home, each module's entries and Settings. On phones it moves into a menu sheet opened from the top bar.
- **Top bar:** where you are as a breadcrumb, the command bar, jobs with a count of running ones, the assistant, appearance and help.
- **Content** in the middle, with one page title (`h1`) per screen.
- **Assistant dock** on the end side when the assistant module is on: a third of the window by default, resizable from a fifth to a half by dragging or with the arrow keys, and closable. On phones it opens as a sheet from the bottom, only when asked for.

Skip links lead to the content, the navigation and the assistant. After moving to another page, focus goes to the new page's title and screen readers hear its name.

## Command bar

⌘K, or Ctrl+K on Windows and Linux, opens the command bar from anywhere. It goes to any page, runs any action a module offers, switches light, dark and high contrast, and with the assistant on, takes a question or one of its / commands. Every action in the panel should be reachable from it.

## Keyboard shortcuts

| Keys | Does |
|---|---|
| ⌘K | Open the command bar |
| ⌘J | Show jobs |
| ⌘. | Open or close the assistant |
| ? | Show the shortcuts |
| G then H | Go to the home screen |
| G then S | Go to the settings |
| Esc | Close a dialog, menu or panel |

Shortcuts without ⌘ don't fire while you type in a field. Modules add their own, which show in the shortcut list and next to their commands.

## The change preview

Every change, from a form, a recipe, an automation, an add-on or the assistant, is shown before it's applied, in one component:

- what happens to each resource, field by field, with old values struck through and secrets hidden
- what happens on the server: packages, services, ports, files, and downloads with their sizes
- how much it costs: download, disk and memory
- what hooks and add-ons added, and any conflicts between them, which block applying until resolved
- how risky it is, and whether a snapshot is taken first

Changes that remove data say where, in the protected colors, and ask for confirmation. Critical ones also ask for the resource's name to be typed. After applying, the preview offers undo.

## Confirmations

Ask before something that loses data or can't be undone, and only then. The dialog says what's affected, the confirm button repeats the action ("Delete site"), and Cancel is focused first. Destructive confirmations use the protected palette, which no theme can change.

## Jobs

Work that takes more than a moment runs as a job and shows in the jobs drawer: running jobs first, with progress, the current step and a live log, then finished ones with their result. Running jobs can be canceled and failed ones retried; while that's under way the button shows it's busy, and if it fails, the job says why and what to do. The count in the top bar shows how many are running.

## Forms

- Labels sit above fields; help text below them, connected for screen readers.
- Fields are checked when you leave them and again on submit. Errors appear under the field and say what to do; on submit, a summary says how many fields need attention and focus goes to the first of them.
- Rarely needed settings fold into an Advanced section, which opens by itself when a problem, the server's included, is in it.
- Secrets are never shown back. When editing, an empty secret field keeps the current value.
- Fields that can only be set when creating something are locked afterwards, with a note saying why.
- Forms for a resource are generated from its schema, so every kind has a working form before it has a custom screen.

## Tables

Tables sort from their headings, search across every column and filter by column. A view (sort, search, filters and hidden columns) can be saved and brought back. Selecting rows shows a toolbar of actions for them. Long lists render only the rows on screen. A table always says how many rows it shows, and separates "nothing here yet" from "nothing matches".

## Empty and loading states

- An empty list says what will appear there and offers one first step.
- Loading shows skeletons in the shape of what's coming, not spinners.
- Data stays live: lists and statuses update as things change on the server, without reloading. After the connection drops, or when the server says the panel fell behind, they load again, so nothing missed stays out of date.
- Where it's safe, the screen shows the result of an action at once and corrects itself if the server disagrees.

## Errors

A screen that can't load says what failed, why, and offers Try again, with technical details folded away for bug reports. A field the server rejects shows the server's reason under that field.

## Toasts

Toasts confirm that something finished: short, in the past tense, with Undo when there is one. They don't hold anything someone has to act on, because they disappear.

## Status

Status is never color alone. A status dot has a label, a badge has words, a trend has an arrow. Success is green, warning amber, danger red and info blue, in every theme.

## Charts

One y-axis per chart. A legend whenever there's more than one series, and each series keeps its color when others are filtered out. Events such as deploys and restarts are marked on the time axis. Every chart can show its data as a table.

## Code and terminals

Code, configuration and logs use the editor, with line numbers, search, folding, bracket matching and problems underlined. The terminal follows the theme's colors and font, and says when it's connected and why a session ended. In both, Tab indents or completes as it would in an editor or a shell; press Esc and then Tab to move on to the next control.

## Motion and density

Motion is quick and quiet: 120 to 320 ms, never decorative, and off for people who reduce motion. Comfortable density suits most screens; compact fits more rows and smaller controls for people who prefer it. Both are personal preferences on top of the theme.

## Right to left

Layouts use logical properties, so start and end swap in right-to-left languages. Icons that point, such as chevrons and arrows, mirror; icons that show real things, such as a clock, don't.
