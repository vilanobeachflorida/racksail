---
title: "Voice"
description: "How Racksail talks: plain words, sentence case and errors that say what to do."
---

Racksail talks like a colleague who knows servers well and respects your time: plain, direct and calm. It tells you what is true, what's about to happen and what to do next, and then gets out of the way. The same voice runs through the panel, the CLI, the installer, emails, the docs and the README.

## Principles

**Plain words.** Write "turn on", not "enable"; "can't", not "is unable to"; "use", not "utilize". Technical terms are fine where the reader uses them too (a DNS record is a DNS record), but don't use one to sound precise.

**Short and in order.** Lead with what matters. One idea per sentence, a few sentences per paragraph. Put the action last: "To keep the old address working, add a redirect."

**Honest.** Say what will happen, including the parts people may not like: what gets downloaded, what restarts, what's removed. Don't promise what isn't certain, and don't hide a cost behind a link.

**Calm.** No exclamation marks, no "Oops", no "Whoa". Success needs no celebration; failure needs no drama.

**Respectful.** No "please" and no "sorry": they add words, not kindness. Never blame the reader. Don't call anything "simple" or "easy"; it may not be for them.

**Sentence case.** Headings, buttons, menu items, tabs and titles capitalize the first word and names only: "Create a site", not "Create A Site".

## Errors

Every error says what happened and what to do, in that order, without codes or jargon in the sentence a person reads. Details for a bug report go under "Details".

| Instead of | Write |
|---|---|
| Error: ACME challenge failed (403). | The certificate couldn't be issued: example.com doesn't point to this server yet. Point its A record to 203.0.113.10, then try again. |
| Invalid input. | Enter a host name, such as mail.example.com. |
| Operation failed. Please contact your administrator. | You don't have permission to change backups. An admin of this server can give it to you. |
| Something went wrong! | The panel isn't answering. Check that racksail is running, then reload this page. |

When the problem is the panel's own, say so and say what's being done about it, if anything.

## Buttons and actions

Buttons name what they do, with a verb: "Create site", "Apply", "Delete mailbox", "Try again". Not "OK", "Yes", "Submit" or "Confirm". The button in a confirmation repeats the action from the question, so someone who reads only the button still knows what it does.

## Confirmations

Ask only before something that loses data or can't be undone. Say what's affected and what happens to it, and whether it can be restored.

> **Delete example.com?** Its files and database are removed. A snapshot is taken first, so it can be restored.
>
> [Cancel] [Delete site]

Don't ask "Are you sure?". If the answer is usually yes, offer undo instead of a question.

## Empty states

Say what will be here and offer the first step, once:

> **No sites yet**
> Sites you create or migrate show up here.
> [Create a site]

## Success

Short, in the past tense, about the thing rather than the process: "example.com is live." "Mailbox created." Offer undo when there is one. Don't use toasts for anything someone has to act on.

## Numbers, dates and units

- Use numerals: 3 sites, not three sites.
- Write sizes with their unit and let the panel format them in the reader's locale: 26 MB, 4 GiB of memory.
- Times are relative when recent ("2 minutes ago") and absolute otherwise, in the reader's time zone, with the zone when it matters.

## Words we use

| Use | Not |
|---|---|
| Racksail | RackSail, Rack Sail, racksail (except the command) |
| the panel | the dashboard, the control panel, the admin |
| sign in, sign out | log in, login (as a verb), log out |
| turn on, turn off | enable, disable, activate |
| set up (verb), setup (noun) | setup (verb) |
| site | website, vhost (in the panel) |
| change, change set | operation, transaction |
| preview, apply, undo | dry run (in the panel), execute, roll back |
| email | e-mail |
| server | box, machine, node (unless it's a cluster node) |

## Writing for translation

Everything in the panel is translated, so write messages that translate well:

- Write whole sentences in one message, with placeholders: "Delete {name}?", not "Delete " followed by a name.
- Use the message format's plurals: "{count, plural, one {# site} other {# sites}}".
- Avoid idioms, puns and cultural references.
- Leave room: some languages need a third more space, and some run right to left.

## The tagline

"Your server, your rules." It goes under or beside the logo, on its own, with its period. Don't work it into sentences or rewrite it.
