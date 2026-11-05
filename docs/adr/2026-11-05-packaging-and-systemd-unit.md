# ADR: packages ship no live configuration and run musterd as a static, confined user

- Status: accepted
- Date: 2026-11-05
- Requirements: OPS-1, OPS-2, SEC-3, SEC-5

## Context

OPS-1 asks for `.deb` and `.rpm` packages that carry the systemd unit and the default
configuration path, and SEC-5 asks that `musterd` run as a non-root user under a hardened
unit. The obvious layout installs the example configuration as `/etc/muster/musterd.json`
and lets the operator edit it in place. The example names secret files (the App key, the
webhook secret, TLS keys) that do not exist on a fresh host, so a unit started before the
edit fails at once and, with `Restart=on-failure`, keeps failing.

## Decision

1. Two packages. `musterd` holds the daemon, the unit, the user definition and
   `/etc/muster`. `musterctl` holds only the CLI, so it installs on any host or workstation.
2. The `musterd` package creates `/etc/muster` (mode 0750, `root:muster`) and leaves it
   empty. The example goes to `/usr/share/doc/muster/musterd.json.example`. The unit has
   `ConditionPathExists=/etc/muster/musterd.json`, and the package does not enable or start
   it.
3. The `muster` user is static, defined in `/usr/lib/sysusers.d/muster.conf` and created by
   `systemd-sysusers` in postinstall. It is not `DynamicUser=`, so file modes on secrets in
   `/etc/muster` stay stable and readable by group.
4. The unit drops everything a stateless network service does not need: no capabilities,
   `ProtectSystem=strict` with no writable paths, IP and Unix sockets only, and the
   `@system-service` syscall filter without `@privileged` and `@resources`.

## Consequences

- Installing the package never starts a daemon that cannot work. The operator copies the
  example, supplies secrets, then runs `systemctl enable --now musterd`.
- Upgrades never touch the operator's configuration, because the package does not own it.
- A later feature that writes to disk, opens a privileged port, or needs a new address
  family or syscall group must relax the unit on purpose, in the same change.
- Manual installs from the release archives follow the same layout; `docs/releasing.md`
  and the install docs give the steps, using the shipped sysusers file.
