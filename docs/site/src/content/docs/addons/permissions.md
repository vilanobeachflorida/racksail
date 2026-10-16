---
title: "Permissions and sandboxing"
description: "What an add-on may read, change and reach, and how the panel enforces it."
---

An add-on gets exactly what its manifest asks for and the person installing it approved, and nothing else. The panel enforces that itself, on every call in both directions, and systemd holds the add-on's process to the same limits.

## The permissions

Permissions go under `permissions` in [addon.yaml](../manifest/):

```yaml
permissions:
  hooks: [validate, modify]
  read:
    - kind: web.site
      fields: [domains, runtime]
  write:
    - kind: backups.policy
      fields: [destinations]
  secrets: false
  network: [api.example.com, 203.0.113.0/24]
  resources: {cpu: 10%, memoryMB: 128}
```

| Permission | What it allows | Without it |
|---|---|---|
| `hooks` | The hook levels the add-on may use: `observe`, `validate`, `modify` and `system` | No hooks. Every hook in the manifest needs its level here |
| `read` | Kinds of resources it may see. A kind on its own covers all of it: the settings, the status and the labels. With `fields`, as dotted paths, it sees those fields of the settings, the status's phase and, with `labels: true`, the labels | It learns which resources change and how, but none of their settings |
| `write` | Kinds it may change, with the same lists. A kind on its own covers whole resources: their settings and labels, deleting them and every action. With `fields`, it may create resources and change those fields, and nothing else unless the scope also has `labels: true`, `delete: true` or the `actions` it may run. Writing a field includes reading it | It changes nothing |
| `secrets` | Reading the secrets of the resources it may read, such as a database user's password. People see it in red when they approve it | Secrets never reach it |
| `network` | Hosts it may connect to: names, IP addresses or networks such as `203.0.113.0/24` | No network at all |
| `resources` | The most CPU, in percent of one CPU, and memory its process may use | 20% of a CPU and 64 MB |
| `override` | `core.override`, which loosens the [invariants](../hooks/#configuration-patches-and-invariants) no hook can otherwise touch | Hooks can only make the invariants stricter |

A scope that allows an action and nothing else names only the action, as `{kind: web.site, actions: [purge]}` does. Asking to delete or run actions under `read` is refused, since they take `write`.

Ask for as little as the add-on's job needs: the fields it reads rather than whole kinds, the hosts it calls rather than the whole internet, and secrets only when it can't work without them.

## Approving them

Installing an add-on is a change like any other: its preview shows what the panel will download, the sandbox it sets up and the permissions the add-on gets, and the change only goes through when the permissions approved cover everything the manifest asks for. Otherwise it's refused with `addons.not_approved`, naming the first permission missing. A new version that asks for more needs the new permissions approved when it's installed.

Three permissions can only be approved by an admin of the whole server, whoever else may install add-ons: `override`, `secrets` and `system` hooks. Someone with a narrower grant, a token or another add-on is refused with `addons.admin_approval`, and so is giving an add-on that holds one of them another program, since the new one would get what was approved for the old. Changing anything else, such as turning the add-on off, needs no admin.

## How the panel holds an add-on to them

**What it's sent.** When the panel calls one of the add-on's hooks, it sends a copy of the change set in which changes of kinds the add-on may read keep only the fields it may read, and their labels only when it may see them, and every other change says only what happens to which resource. Actions' inputs and outputs, and the steps the agent will run, are left out. The actor making the change is sent without the address they came from.

**What it may send back.** A modify hook answers with the change set it changed. The panel takes from the answer only the fields the add-on may write, of the changes the hook point is about, the changes it added of kinds it may write, and the effects it added; a change it added that deletes or sets labels needs the grant for that too, and hooks can't add actions. Anything else it changed is a failure of the hook, and its work is thrown away, so the hook's failure mode decides whether the change goes ahead without it. Everything taken is labelled with the add-on's name and version in the preview and the audit log, and undone with the rest of the change set.

An update or delete the add-on adds is about the resource it names, as the change set leaves it and wherever it is: an account's add-on can't reach another account's resources, whatever scope its answer claims. An update holds the resource's settings as the add-on reads them, with its changes, as its answer does for the changes it was sent: the fields it may write are taken from it, the fields it can't read stay as they are, and changing any other field fails the hook. Labels in an update replace the resource's, so sending any, even none, needs the grant for labels unless they're the ones it has. The panel checks what it took once more as it plans the change, against the resource's own scope, settings and labels.

**What it may ask.** The add-on calls the panel's API as itself, an actor of type add-on whose grants are its permissions. Answers keep only what it may read, and a list of a kind it sees only part of can't be searched, filtered or sorted by the rest, `addons.query_denied`. A change it submits is refused with `addons.field_denied` when it touches a field it may not write, and with `addons.delete_denied`, `addons.action_denied` or `addons.labels_denied` when it deletes a resource, runs an action or changes labels without the grant for it. Its requests go through the change pipeline like anyone's, so rules and other hooks apply to them, and the audit log records them as the add-on's.

## Add-ons of an account

An account's owner can install add-ons for their account, `core.account_addon` resources, which only ever see and change that account:

- They attach only to points of the change pipeline, `<kind>.<operation>.<stage>` or patterns of them, never to lifecycle, sign-in or capability points, which belong to the whole server.
- They hear only about their account's changes. A change set that reaches other accounts comes to them with those changes emptied, and without the title, effects and estimate of the whole.
- They can't ask for `system` hooks or `override`, or provide a capability.
- They come from a catalog: the manifest must be the one the catalog's signed index lists for the item, and the program one its recipe pins. See [Publishing add-ons](../publishing/).
- They run in the account's slice, `racksail-accounts-<slug>.slice`, within the account's plan, so the memory and CPU they're given never go beyond what the plan allows the whole account. Until the module that keeps accounts says where an account's services run, installing one is refused with `addons.no_account`.

## The sandbox

Every add-on runs as its own systemd service, `racksail-addon@<id>.service`, started by systemd the first time the panel connects to its socket, `/run/racksail/addons/<id>.sock`, which only the panel can open. The service runs:

- as a user systemd makes up for it, with no capabilities and no way to gain any;
- with a read-only view of the system, no home directories, its own `/tmp` and devices, and the kernel's tunables, modules, logs, control groups and clock out of reach;
- without the panel's state, the agent's and the edge's sockets, or `/etc/racksail`, which aren't there at all;
- with system calls limited to those services need, and none that are privileged, mount, load modules, change the clock or reboot;
- with its memory capped and no swap, its CPU time capped, and at most 64 tasks;
- with a directory of its own to keep things in, `/var/lib/racksail-addon/<id>`.

**Network.** Without `network`, the add-on has no network at all, only Unix sockets. With it, it may connect to the declared hosts only: names are looked up each time the add-on is reconciled, and their addresses are the only ones its service may reach, through a hosts file of its own that knows those names and nothing else. When an address changes, the service starts again with the new one.

**The panel's API.** The add-on reaches the panel at the socket named by `RACKSAIL_PANEL_SOCKET`, `/run/racksail/addons.sock`, with the token systemd hands it as the credential `token`, in `$CREDENTIALS_DIRECTORY/token`. A token only works there, only while the add-on runs, and is replaced with every new version; the panel's database keeps only its hash. See [SDKs](../sdks/#call-the-panel).

**Crashes.** A service that fails is started again after three seconds, five times in two minutes at most. An add-on that crashes three times within two minutes of starting turns [safe mode](../hooks/#safe-mode) on, which stops every add-on until an admin turns it off.
