---
title: "A tour of the panel"
description: "The icon rail, the command bar, the home screen, jobs and change previews."
---

The panel is one page with the same frame around every screen.

## The frame

- **The icon rail** on the left has a button for each module that's on: sites, mail, DNS, apps, storage and the rest. Modules that are off don't appear.
- **The top bar** holds the command bar, your notifications and your profile.
- **The content area** shows the screen you're on.
- **The assistant dock** takes the right third of the window when the assistant module is on. You can resize it, collapse it, or on a phone pull it up from the bottom.

Every screen works with the keyboard alone. <kbd>Tab</kbd> reaches every control, the first <kbd>Tab</kbd> on a page offers **Skip to content**, and focus is always visible.

## The command bar

Press <kbd>⌘K</kbd>, or <kbd>Ctrl+K</kbd> on Windows and Linux, from anywhere. Type to:

- go to any screen or resource: "blog.example.com", "mailboxes", "backups"
- run an action: "new site", "restart php", "renew certificate"
- ask the assistant a question, when it's on
- use a `/` command, such as `/undo`

The command bar only offers what your role allows.

## Home

The home screen starts with the **attention inbox**: the things that need you, such as a certificate that couldn't renew, a disk filling up, a failed backup or a pending security update. Each item says what happened and what to do, with a button that does it. Below are quick actions and recent activity across the server.

## Lists and details

Every kind of resource has a list and a detail screen. Lists can be searched, filtered, sorted and saved as views, and they act on several items at once. Detail screens show the resource's settings, its status with the reason for it, its history of changes and its events.

## Change previews

When you save a form or run an action, you see the **change preview** before anything happens: the fields that change, and what happens on the server, such as downloads with their sizes, services started and ports opened. Contributions from rules and add-ons are labelled with their source. Choose **Apply** to go ahead.

## Jobs

Applied changes run as jobs. The **jobs drawer** shows running and recent jobs with live logs; you can cancel a running job or retry a failed one. When a change finishes, a notification offers **Undo**.

## Your settings

Your profile holds your password, passkeys and two-factor sign-in, your sessions and API tokens, your SSH keys, which notifications you get and where, and how the panel looks: light, dark or the system's choice, high contrast, density, text size and reduced motion.
