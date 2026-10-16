# Security

Racksail runs as root on machines connected to the internet and is trusted with other people's websites, email and files. Control panels are among the most attacked software there is. These rules aren't optional.

## Threat model

| Threat | Main defenses |
|---|---|
| A customer attacks another customer on the same server | Linux users per account, sandboxed systemd services, per-account slices and quotas, rootless containers, private /tmp, socket binding limits, per-account outbound IP |
| The internet attacks the panel | Small attack surface, authentication on everything, rate limits, lockouts, passkeys and TOTP, step-up confirmation for sensitive actions, CSRF protection, strict Content Security Policy, secure cookies, optional IP allowlists, fail2ban-style bans in nftables |
| A compromised site tries to take the server | The site runs as its account user in a sandbox with no access to other accounts, the panel or the agent socket |
| A compromised panel tries to take the server | The panel runs unprivileged; the agent accepts only typed, validated operations from a fixed allowlist |
| A malicious or careless add-on, recipe or theme | Declared permissions enforced at runtime, sandboxed processes, no network unless declared, preview and audit of every change, safe mode, signed catalogs, no code in themes, no custom CSS from catalogs |
| Prompt injection through logs, emails, files or DNS records reaching the assistant | Untrusted content marked as data; the assistant has the user's permissions only; every change goes through preview and approval; no free-form command, HTTP or email tools; secrets never reach the model |
| Supply chain | Pinned upstream sources, signature and checksum verification, a delay before new upstream releases reach the catalog, signed Racksail releases with SBOMs, dependency scanning, license allowlist |
| Someone at the console | The console menu requires the admin password for anything beyond showing status; safe mode and password reset are logged |

## Rules for code

1. The agent exposes only typed operations. Each validates every argument (paths stay inside allowed roots, names match strict patterns, sizes are bounded), and every call, refused ones included, is recorded in the agent's audit trail. Other servers' agents are reached only with mutual TLS, with certificates that name who holds them.
2. No shell. External programs run with argument vectors only.
3. Every API operation checks permissions through the central authorizer. A test enumerates every operation and fails if one isn't covered.
4. Every mutation goes through the change pipeline, so it is validated, hooked, previewed (when interactive) and audited.
5. Secrets are encrypted at rest, redacted in logs, API responses and assistant context, and only decrypted where they're used.
6. Uploaded and imported files are size-limited, checked for path traversal, and parsed by fuzzed parsers.
7. Templates (emails, error pages, generated configs) escape by default.
8. Sessions: HTTP-only, Secure, SameSite=Strict cookies; rotation on login and privilege change; idle and absolute timeouts.
9. Rate limits and lockouts on every authentication endpoint, including SFTP, SSH and mail authentication (through nftables bans).
10. Default-deny firewall. Each module opens only the ports it needs, and only while it's active.
11. New dependencies are reviewed for license and maintenance.

## Sandboxing

Site and worker services run with at least:

`User=` the account user, `ProtectSystem=strict`, `ProtectHome=tmpfs` with the account's own directory bound in, `PrivateTmp=yes`, `PrivateDevices=yes`, `NoNewPrivileges=yes`, `ProtectKernelTunables=yes`, `ProtectKernelModules=yes`, `ProtectControlGroups=yes`, `RestrictSUIDSGID=yes`, `RestrictNamespaces=yes`, `LockPersonality=yes`, `MemoryDenyWriteExecute=yes` where the runtime allows it, `SystemCallFilter=@system-service`, and `SocketBindAllow=` for the site's own socket or port.

The agent's socket, the edge's admin socket and the panel's edge-facing socket are Unix sockets under `/run/racksail`, which `InaccessiblePaths=-/run/racksail` hides from the sandbox, so reaching them doesn't depend on an address rule. The sandbox denies no addresses by default, because denying the server's own addresses would cut a site off from its database on the same server, which systemd can't tell apart by port. That works only because none of those privileged channels is reached by address: the panel's own port (8443) authenticates every request and, crucially, trusts a forwarded client address only on the `/run/racksail` socket the edge proxies through, never from a loopback peer, so a site reaching the port over loopback can't forge the address the allowlist, the rate limiter and the lockouts key on. A module that keeps the panel on a management address of its own denies that address with `IPAddressDeny=`. The rendered profile is `system.Sandbox` in `internal/system`, and the system layer page in the documentation site explains the reasoning.

End-of-life runtimes (for example PHP 5.x and 7.x) always get the strictest profile, the web application firewall in blocking mode, and a permanent warning badge.

## The assistant

- It acts with the signed-in user's permissions, through the same API.
- Every change it proposes is a change set the user reviews and applies. Permission modes: ask every time (default), auto-approve reversible changes, read-only.
- Tool results from logs, files, emails, DNS records and other outside sources are marked as untrusted data in the model context.
- It has no tool that runs arbitrary commands, sends arbitrary HTTP requests or sends email. If an admin enables the optional shell tool, every command needs individual approval.
- Secrets are replaced with placeholders before anything reaches a model.
- Admins choose which providers each account may use and can require local models only.

## Reporting vulnerabilities

See [SECURITY.md](../../SECURITY.md).
