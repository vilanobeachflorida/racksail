---
title: "Secrets"
description: "How passwords, keys and tokens are kept encrypted, and where their key lives."
---

Passwords, keys and tokens never appear in a resource's settings, its history, previews or logs. Settings hold a reference to a secret, and the value is looked up only when it is needed.

## At rest

Each secret is kept in its own file in the panel's state, encrypted with AES-256-GCM. The secret's name is part of the encryption, so encrypted values can't be swapped between secrets.

## The key

The key is sealed with `systemd-creds`: bound to the server's TPM2 chip when it has one, and to the server's host key always, so it can only be opened on that server. systemd opens it when it starts the panel and hands it to the panel alone. The seal doesn't depend on firmware measurements, so a firmware or Secure Boot update doesn't lock the panel out.

The panel makes the key the first time it starts. Where it can't be sealed, the key is kept in a file only the panel can read. A key that doesn't open the secrets already kept is never used, and the panel stops rather than make a new key while there are secrets the missing one would open, so restoring the key file or the sealed credential brings everything back.

In development, the simulated server seals the key the same way and keeps it between runs.
