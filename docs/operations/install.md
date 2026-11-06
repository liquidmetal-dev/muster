# Installing musterd

musterd runs as the system user `muster` under the systemd unit `musterd.service`. The unit
reads `/etc/muster/musterd.json`. It does not start until that file exists. The
[packaging ADR](../adr/2026-11-05-packaging-and-systemd-unit.md) gives the reasons.

## From the deb or rpm package

1. Download `musterd` (and `musterctl` if you want it on the host) for your architecture
   from the GitHub release. Compare each file with `checksums.txt`.
   In v0.1.0, `musterctl` only prints its version. Its commands come with the gRPC API
   ([#23](https://github.com/liquidmetal-dev/muster/issues/23)).
2. Install the packages. Set `VERSION` to the release that you downloaded, without the `v`:

   ```sh
   VERSION=0.1.0   # the release that you install
   sudo apt install ./musterd_${VERSION}_amd64.deb ./musterctl_${VERSION}_amd64.deb   # Debian, Ubuntu
   sudo dnf install ./musterd-${VERSION}-1.x86_64.rpm ./musterctl-${VERSION}-1.x86_64.rpm   # Fedora, RHEL
   ```

   The package creates the user `muster` and an empty `/etc/muster` (mode 0750, `root:muster`).
   It does not enable or start the unit.

3. Copy the example configuration and edit it:

   ```sh
   sudo install -m 0640 -g muster /usr/share/muster/musterd.json.example /etc/muster/musterd.json
   ```

4. Put each secret file (the App private key, the webhook secret, TLS keys) in `/etc/muster`
   with mode 0640 and owner `root:muster`. musterd runs as `muster`, so it reads the files
   through the group.
5. Enable and start the unit, then read its log:

   ```sh
   sudo systemctl enable --now musterd
   journalctl -u musterd
   ```

An upgrade of the package does not change the files in `/etc/muster`. See
[upgrade.md](upgrade.md) for the upgrade procedure.

## From the archive

The `musterd` archive holds the binary, the unit, the sysusers file and the example
configuration. Install them at the same paths that the package uses:

```sh
sudo install -m 0755 musterd /usr/bin/musterd
sudo install -m 0644 deploy/systemd/musterd.service /usr/lib/systemd/system/musterd.service
sudo install -m 0644 deploy/systemd/muster.sysusers /usr/lib/sysusers.d/muster.conf
sudo systemd-sysusers /usr/lib/sysusers.d/muster.conf
sudo install -d -m 0750 -g muster /etc/muster
sudo install -D -m 0644 docs/examples/musterd.json /usr/share/muster/musterd.json.example
sudo systemctl daemon-reload
```

Then do steps 3 to 5 of the package procedure.

## In a container

The image `ghcr.io/liquidmetal-dev/musterd` runs musterd as UID 65532. Mount the
configuration and the secrets read-only, and make sure that UID 65532 can read them:

```sh
VERSION=0.1.0   # the release that you install
docker run -d --name musterd \
  -v /srv/muster:/etc/muster:ro \
  -p 8443:8443 -p 9443:9443 -p 9090:9090 \
  ghcr.io/liquidmetal-dev/musterd:${VERSION}
```

## Unit hardening

The unit gives musterd no capabilities and makes the file system read-only
(`ProtectSystem=strict`). musterd can use only IP and Unix sockets, and the
`@system-service` syscall set without `@privileged` and `@resources`. musterd keeps no
state, so it does not need a writable directory. If a later feature writes to disk, opens
a port below 1024, or needs a different syscall group, that change must also change the
unit.
