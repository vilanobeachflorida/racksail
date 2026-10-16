---
title: "The racksail command"
description: "Manage servers from a terminal with the racksail command, which covers every API operation."
---

The `racksail` binary that runs your server is also a command-line client for any Racksail panel. It has a command for every operation of the API that [API tokens](../../security/api-tokens/) may use, built from the API's own description, so a new module's resources get commands as soon as the module is there.

```sh
racksail account list
racksail webhook create --url https://hooks.example.com/racksail --events '*.failed' --secret @secret.txt
racksail job watch job_01j9…
```

## Sign in

Make an API token in the panel, under **Settings → API tokens**, then sign in with it:

```sh
racksail login --server https://panel.example.com
```

The command asks for the token without showing it, checks it with the panel, and keeps it in a **profile** named after the panel's host. Later commands call that panel with it. In scripts, pass the token on standard input instead:

```sh
racksail login --server https://panel.example.com --profile work --token-stdin < token.txt
```

Profiles are kept in `racksail/cli.json` in your configuration directory (`~/.config` on Linux, `~/Library/Application Support` on macOS), which only you may read: the command refuses to use a file other users can read. Manage them with:

```sh
racksail profile list          # the profiles, the current one starred
racksail profile use work      # make one the current one
racksail logout                # forget the current profile and its token
```

`racksail whoami` says who the token signs in as, what it may do and which panel it's talking to. `racksail status` says whether the panel is healthy and which version it runs.

### Without a profile

Every client command takes these flags and environment variables, which win over the profile:

| Flag | Environment | What it does |
|---|---|---|
| `--profile` | `RACKSAIL_PROFILE` | Uses another profile than the current one |
| `--server` | `RACKSAIL_SERVER` | The panel's address. Without either, the profile's, or `http://127.0.0.1:7070` on the server itself |
| | `RACKSAIL_TOKEN` | The API token to send. A profile's token is only ever sent to the profile's own panel |
| `--ca-file` | | A certificate authority to trust, for a panel with its own certificate; `login` keeps it in the profile |
| | `RACKSAIL_CONFIG` | Where profiles are kept, instead of `racksail/cli.json` |

The token is never a flag, so it stays out of your shell's history.

## Commands

Commands are named after what they act on, then what they do:

- **Resources** are `racksail <kind> <operation>` for the core's kinds and `racksail <module> <kind> <operation>` for a module's: `racksail account list`, `racksail webhook create`, `racksail dns zone get`. Each kind has `list`, `get`, `create`, `update`, `delete` and `history`, and a command per action, such as `racksail webhook test`.
- **The rest of the panel** has commands of its own: `racksail user list`, `racksail role list`, `racksail changeset undo`, `racksail job logs`, `racksail event watch`, `racksail audit export`, `racksail me show`, `racksail health`.

`racksail --help` lists them, and `racksail <command> --help` says what one does, what it needs and its flags, with an example. Operations only people in the panel may use, such as signing in or changing how someone signs in, have no command.

### Arguments and flags

What an operation is about comes first, as arguments: `racksail user get user_01j9…`. Everything else is a flag:

- **The request's fields**, a resource's settings included: `--name`, `--domains`, and the fields inside them joined with dashes, such as `--limits-memory-max`.
- **Lists** take commas or the flag again: `--events 'web.site.*,*.failed'` or `--events 'web.site.*' --events '*.failed'`.
- **Maps** take `key=value`, again for each: `--labels team=ops --labels env=prod`.
- **Sizes** take bytes or units, counting in 1024s as systemd does: `512M`, `2G`.
- **Owners** take `server`, `account:ID`, `reseller:ID` or an account's ID on its own.
- **Roles** take `ROLE` or `ROLE@OWNER`: `--grants admin`, `--grants owner@acct_01j9…`.
- **Secrets**, such as a webhook's signing secret, go apart from the settings, as in the panel. Read them from a file with `@file`, or from standard input with `-`, so they stay out of your history: `--secret @secret.txt`.

For anything the flags don't reach, give the whole request as JSON or YAML with `--file` (`-` reads standard input), and change single fields with `--set path=value`, where values that read as JSON are JSON:

```sh
racksail webhook create --file hook.yaml --set spec.disabled=true
```

An **update** starts from what the panel has now, with its revision, so flags only change what they name, and someone else's change in between makes it fail instead of being lost. `--replace` sends only what you give.

## Output

Answers print as tables by default. `-o json` prints the panel's JSON and `-o yaml` YAML, for scripts:

```sh
racksail account list -o json | jq -r '.items[].spec.slug'
```

Lists come a page at a time; the table says how to get the next one with `--cursor`, and `--all` reads every page. `racksail audit export` writes CSV or JSON lines, as `--format` says, to standard output.

## Changes

Every change goes through the change pipeline. `--dry-run` shows the preview without changing anything: each change with the fields it sets, what happens on the server, what hooks and add-ons added, and what it costs.

```sh
racksail account open --slug acme --name "Acme Inc." --limits-memory-max 2G --dry-run
```

Without `--dry-run` the command shows the same change set, then follows the job that carries it out, with its log as it comes and a progress line, until it's done. A job that fails makes the command fail with its reason. Ctrl-C stops following, not the job; `racksail job watch` follows it again. `--detach` returns as soon as the change is accepted, and with `-o json` the command prints the change set and waits quietly, so its exit status says how the job went.

Operations that aren't changes to resources, such as adding a user, show the request they would send under `--dry-run`, with secrets hidden, and send nothing.

## Live streams

`racksail event watch`, `racksail resource watch` and `racksail job watch` print what happens as it happens, until you press Ctrl-C. They pick up where they left off when the connection drops. With `-o json` each event is a line of JSON.

## Shell completion

```sh
racksail completion bash > /etc/bash_completion.d/racksail
racksail completion zsh > "${fpath[1]}/_racksail"
racksail completion fish > ~/.config/fish/completions/racksail.fish
```

Besides commands and flags, completion offers the panel's own IDs, such as `racksail user get <Tab>` listing users by name, and the values a flag takes.
