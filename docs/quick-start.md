# Quick start

This page takes you from an empty host to one GitHub Actions job in a muster microVM. It uses
one flintlock host, one battery, one GitHub repository and a GitHub App, and it links to the
other pages for the details.

> **Status:** musterd startup arrives with
> [#38](https://github.com/liquidmetal-dev/muster/issues/38). Until then, `musterd` checks the
> configuration and exits, so step 8 cannot run a job yet.

## 1. What you need

- A Linux host with KVM (`/dev/kvm`) for flintlock and Firecracker.
- A host for poolmgrd (battery) and musterd. It can be the flintlock host.
- A GitHub repository where you are an admin. This page uses `you/your-repo`.
- An HTTPS address that GitHub can reach, such as `https://muster.example.com`, for the
  webhook. If GitHub cannot reach your host, use the [polling configuration](#no-inbound-access)
  in step 6.

## 2. Prepare the flintlock host

Follow flintlock's guides to install the host:

- [Firecracker](https://github.com/liquidmetal-dev/flintlock/blob/main/userdocs/docs/getting-started/firecracker.md)
- [containerd](https://github.com/liquidmetal-dev/flintlock/blob/main/userdocs/docs/getting-started/containerd.md),
  with a devmapper thin-pool
- [Network](https://github.com/liquidmetal-dev/flintlock/blob/main/userdocs/docs/getting-started/network.md)

muster needs flintlockd v0.15.2 or later, started with the exec API:

```sh
flintlockd run --enable-exec-api --parent-iface <host-interface>
```

`--parent-iface` (or `--bridge-name`) comes from the network guide. This page leaves out
flintlock's TLS flags; for a production host, set them and the matching
[`flintlock.tls`](configuration.md#flintlock) in musterd. Each VM uses space in the
thin-pool: see [capacity](operations/capacity.md) for its size.

## 3. Run battery and register the host

Run poolmgrd as battery's documentation describes
([battery](https://github.com/liquidmetal-dev/battery)). Then register the flintlock host
under the name `host-a`:

```sh
poolmgrctl --addr battery.example.com:9091 host add host-a --address <flintlock-host>:9090
poolmgrctl --addr battery.example.com:9091 host get host-a
```

The name `host-a` is the name that musterd uses for the host. See [hosts](operations/hosts.md).

## 4. Create the GitHub App

1. Open the settings of the account that owns `you/your-repo`: **Settings > Developer
   settings > GitHub Apps > New GitHub App**.
2. Set the webhook URL to `https://muster.example.com/webhook`. Make a random webhook secret
   and keep it, for example with `openssl rand -hex 32`.
3. Give the App these repository permissions:
   - **Administration**: Read and write
   - **Actions**: Read
   - **Metadata**: Read (GitHub selects it)
4. Under **Subscribe to events**, select **Workflow job**.
5. Create the App. Note its **App ID**, and generate and download a **private key**.
6. Install the App on `you/your-repo` only. The number at the end of the installation's URL
   is the **installation ID**.

For an organization pool instead of a repository pool, change these items:

| Item | Repository pool | Organization pool |
| --- | --- | --- |
| App permission | Repository: Administration (read and write) | Organization: Self-hosted runners (read and write) |
| Pool scope | `"scope": {"repo": "you/your-repo"}` | `"scope": {"org": "your-org"}` |
| Runner group | not used | `"runner_group": "<name>"`, optional |

[GitHub App permissions](configuration.md#github-app-permissions) gives the reasons for each
permission. To use a personal access token instead of an App, see
[personal access token](configuration.md#personal-access-token).

## 5. Install musterd

Install the package and the unit as [install](operations/install.md) describes. Put these
files in `/etc/muster`, with mode 0640 and the owner `root:muster`:

- The App private key from step 4.
- The webhook secret from step 4.
- The certificate and key that musterd serves the webhook with, for the name in the webhook
  URL. Leave them out if a TLS terminator is in front of musterd (see step 6).
- The CA certificate that signs poolmgrd's server certificate. If poolmgrd uses a
  certificate from a public CA, leave it out and remove `ca_file` from `battery.tls` in
  step 6.

```sh
sudo install -m 0640 -g muster app.private-key.pem /etc/muster/app.pem
printf '%s' '<webhook secret>' | sudo install -m 0640 -g muster /dev/stdin /etc/muster/webhook.secret
sudo install -m 0640 -g muster muster.example.com.crt /etc/muster/tls.crt
sudo install -m 0640 -g muster muster.example.com.key /etc/muster/tls.key
sudo install -m 0640 -g muster poolmgrd-ca.pem /etc/muster/battery-ca.pem
```

## 6. Configure musterd

Write `/etc/muster/musterd.json`. Change the App ID, the installation ID, the repository and
the addresses to your values:

```json
{
  "listen": {"addr": ":8443", "tls": {"cert_file": "/etc/muster/tls.crt", "key_file": "/etc/muster/tls.key"}},
  "github": {
    "app": {"app_id": 123456, "installation_id": 7890123, "private_key_file": "/etc/muster/app.pem"},
    "webhook_secret_file": "/etc/muster/webhook.secret"
  },
  "battery": {"addr": "battery.example.com:9091", "tls": {"ca_file": "/etc/muster/battery-ca.pem"}},
  "flintlock": {"tls": {"insecure": true}},
  "pools": [
    {
      "name": "quick-start",
      "scope": {"repo": "you/your-repo"},
      "labels": ["self-hosted", "muster"],
      "size": 1,
      "flintlock_hosts": ["host-a"],
      "microvm_template": {
        "vcpu": 2,
        "memoryInMb": 4096,
        "kernel": {"image": "ghcr.io/liquidmetal-dev/firecracker-kernel:6.1-20260929-1349", "filename": "boot/vmlinux"},
        "rootVolume": {"id": "root", "sizeInMb": 20480, "source": {"containerSource": "ghcr.io/liquidmetal-dev/muster-runner:minimal-latest"}},
        "interfaces": [{"deviceId": "eth1", "type": "TAP"}]
      }
    }
  ]
}
```

- `listen.tls` makes musterd serve HTTPS on port 8443 with your certificate. You can instead
  leave out `listen.tls` and put a TLS terminator, such as a reverse proxy, in front of musterd
  (see [listen](configuration.md#listen)). GitHub must not reach a plain HTTP port.
- The kernel and the image are the reference ones. See the [image contract](image-contract.md).
- `"flintlock": {"tls": {"insecure": true}}` matches a flintlockd started without TLS, as in
  step 2.
- Each field and its default is in the [configuration reference](configuration.md).

### No inbound access

If GitHub cannot reach your host, use the polling intake. Leave out `listen` and the webhook
secret, and list the repository in `github.poll.repos`. You do not need a webhook URL on the
App. musterd then sees a queued job up to `github.poll.interval` (default `1m`) later.

```json
{
  "github": {
    "app": {"app_id": 123456, "installation_id": 7890123, "private_key_file": "/etc/muster/app.pem"},
    "poll": {"enabled": true, "repos": ["you/your-repo"]}
  },
  "battery": {"addr": "battery.example.com:9091", "tls": {"ca_file": "/etc/muster/battery-ca.pem"}},
  "flintlock": {"tls": {"insecure": true}},
  "pools": [
    {
      "name": "quick-start",
      "scope": {"repo": "you/your-repo"},
      "labels": ["self-hosted", "muster"],
      "size": 1,
      "flintlock_hosts": ["host-a"],
      "microvm_template": {
        "vcpu": 2,
        "memoryInMb": 4096,
        "kernel": {"image": "ghcr.io/liquidmetal-dev/firecracker-kernel:6.1-20260929-1349", "filename": "boot/vmlinux"},
        "rootVolume": {"id": "root", "sizeInMb": 20480, "source": {"containerSource": "ghcr.io/liquidmetal-dev/muster-runner:minimal-latest"}},
        "interfaces": [{"deviceId": "eth1", "type": "TAP"}]
      }
    }
  ]
}
```

## 7. Start musterd

```sh
sudo systemctl enable --now musterd
journalctl -u musterd -f
curl -s localhost:9090/readyz
```

At startup, musterd makes the pool `quick-start` in battery, and battery boots one warm VM.
The first boot can take some minutes, because flintlock pulls the kernel and the image. When
`/readyz` returns 200, musterd is ready. [Observability](operations/observability.md) explains
the logs and the checks.

## 8. Run a first workflow

Add `.github/workflows/muster.yml` to `you/your-repo`:

```yaml
name: muster
on: workflow_dispatch
jobs:
  hello:
    runs-on: [self-hosted, muster]
    steps:
      - run: uname -a
```

Run it from the **Actions** tab. musterd claims the warm VM, and a runner with a name such as
`muster-muster-quick-start-<session id>` comes online in **Settings > Actions > Runners**. The
job runs and goes green. Then the runner goes away, battery deletes the VM, and battery boots
a new warm VM.

## Next

- [Configuration reference](configuration.md): every field.
- [Upgrade](operations/upgrade.md): `musterctl drain --wait`, then a restart.
- [Observability](operations/observability.md): logs, metrics and health checks.
- [Manual end-to-end runbook](runbooks/e2e-manual-verification.md): check a host from end to
  end.
