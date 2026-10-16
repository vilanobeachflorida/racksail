---
title: "A tour of the panel"
description: "The icon rail, the command bar, the home screen, resources, change previews, undo, jobs and events."
---

The panel is one page with the same frame around every screen.

## The frame

- **The icon rail** on the left has a button for each module that's on: sites, mail, DNS, apps, storage and the rest. Modules that are off don't appear.
- **The top bar** holds the command bar, the jobs that are running, the appearance menu and help, and a breadcrumb of where you are.
- **The content area** shows the screen you're on.
- **The assistant dock** takes the right third of the window when the assistant module is on. You can resize it, collapse it, or on a phone pull it up from the bottom.

Every screen works with the keyboard alone. <kbd>Tab</kbd> reaches every control, the first <kbd>Tab</kbd> on a page offers **Skip to content**, and focus is always visible. Press <kbd>?</kbd> for the list of shortcuts.

## The command bar

Press <kbd>⌘K</kbd>, or <kbd>Ctrl+K</kbd> on Windows and Linux, from anywhere. Type to:

- go to any screen: "jobs", "events", "webhooks", "settings"
- find a resource of any kind by its name, address or ID, and **See every result** on the search screen
- start a new resource of any kind you may create: "new webhook"
- run what the resource on screen offers, such as editing it, an action of its kind or deleting it
- open again what you opened recently
- undo the last change you made
- run an action a module offers: "new site", "restart php"
- ask the assistant a question, when it's on, or use one of its `/` commands

The command bar only offers what your role allows.

## Home

The home screen starts with the **attention inbox**: what needs you, most urgent first. A job that failed, a site that stopped working, an alert, package updates waiting, a restart that would finish installing them, a certificate that couldn't be renewed or expires soon, and a module that would help, such as storage for disks nothing uses yet. Each item says what happened, with buttons that do something about it: retry the job, undo the change that failed, open the resource, show the log or the events, or read about it.

Items stay until what they're about is fixed. You can dismiss one from its menu, and it stays away until it changes, as the same job failing again does, or snooze it for a day or a week. **Show dismissed** lists them again to bring one back. The inbox keeps itself current while you look at it.

Below are the panel's status, quick actions, which include starting a new resource of the kinds you may create, and recent activity across the server.

## Resources

Everything Racksail manages is a resource of some kind: a site, a mailbox, a DNS zone, a webhook. Each kind has screens, which a module may replace with its own:

- **The list** shows every resource you can see, with the columns that matter for the kind and its state. Search it, filter it, sort it and save the result as a view. Choose several rows to delete them, or to run one of the kind's actions on all of them at once, with the same input for each when the action asks for some.
- **The detail screen** shows the resource's settings and details, its state with the reason for it and the conditions behind it, the **history** of its settings, each version with what it changed and who changed it, and its **events**. Its actions are in the **Actions** menu.
- **The form** creates a resource or changes it, with help under each field, the fields few people need under **Advanced**, and your own labels. A field that refers to another resource lists the ones you can choose. Secret values, such as a webhook's signing secret, are never shown again; leave them empty when editing to keep them. If someone else changes the resource while you have its form open, the form says what they changed, and the panel won't apply your changes over theirs: choose **Load the latest settings** and make yours again.

**Resources** in the command bar lists every kind, grouped by module.

## Change previews

When you save a form, delete something or run an action, you see the **change preview** before anything happens: the fields that change, and what happens on the server, such as downloads with their sizes, services started and ports opened. Contributions from rules and add-ons are labelled with their source. Choose **Apply** to go ahead. If something changed while you looked at the preview, the panel shows you what the change would do now, so you apply what you saw.

## Undo

Applied changes run as jobs, and a notification follows each one: how far it got, and how it ended. When it's done, the notification offers **Undo**. You can also undo a change later from the resource's history, from the **Changes** screen, or from the command bar. Undoing something that would delete what the change created shows you its preview first. Undo shows when your role allows what it takes: undoing a creation deletes what was made, and undoing a deletion creates it again.

## Jobs, changes and events

- The **jobs drawer**, from the top bar or <kbd>⌘J</kbd>, shows running and recent jobs with live logs; you can cancel a running job or retry a failed one. **All jobs** opens the jobs screen, with every job and its whole log.
- The **Changes** screen lists every change made through the panel, the API, the `racksail` command and the assistant: who made it, what it touched, how risky it was, and **Undo** while it can be undone. It can also list previews that were never applied, to apply one later. Previews don't keep secret values, so one that carried a secret, such as a webhook's signing secret, can't be applied later; its screen links to the form to make the change again.
- The **Events** screen is the timeline of everything that happened that you can see, newest first and live. Narrow it by how urgent, by kind, by event type, by resource or by time; the address keeps your filters, so you can share the view.

## Your settings

**Settings → General** holds your language, the time zone and clock times show in, whether single-key shortcuts such as <kbd>G</kbd> then <kbd>H</kbd> are on (turn them off if they get in the way of speech input or a screen reader), and whether the command bar remembers what you opened. **Settings → Appearance** holds light, dark or the system's choice, high contrast, density, text size and reduced motion, and shows the themes in effect: Racksail's, the server's and your reseller's when there are any, with your choices on top.

Once you're signed in, these settings are kept with your account as well as in the browser, so they follow you to every browser you sign in on. What the command bar remembers you opened stays in each browser. The panel shows your profile's language, unless you choose another one for a browser in **Settings → General**; saving your profile makes that browser follow the profile again.

Your profile holds your password, passkeys and two-factor sign-in, your sessions and API tokens, your SSH keys, and which notifications you get and where.
