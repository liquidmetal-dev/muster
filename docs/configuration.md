# Configuration reference

musterd reads one JSON file. This page lists every field, its default and the rules that
musterd checks at startup. [examples/musterd.json](examples/musterd.json) is a full example,
and the [quick start](quick-start.md) builds a minimal one.

## Loading

- Start musterd with `musterd -config <path>`. The default path is `/etc/muster/musterd.json`.
- An unknown field is an error. A field with a default can be left out.
- Durations are strings such as `"30s"`, `"10m"` or `"6h"`. A JSON number is an error.
- Each secret comes from a file (`*_file`) or from an environment variable (`*_env`). Set one
  of the two, never both. musterd trims white space from the value, and an empty value is an
  error.
- musterd reads every secret and every TLS file at startup. A change to one of them needs a
  restart.
- If the configuration is not valid, musterd prints every error with the path of its field
  and exits with code 2.

## Top level

| Field | Type | Default | Notes |
| --- | --- | --- | --- |
| `listen` | object | none | The webhook intake. See [listen](#listen). |
| `api` | object | none | The gRPC API. See [api](#api). |
| `metrics_addr` | host:port | `:9090` | Serves `/metrics`, `/healthz` and `/readyz`. See [observability](operations/observability.md). |
| `log` | object | | |
| `log.format` | string | `json` | `json` or `text`. |
| `log.level` | string | `info` | `debug`, `info`, `warn` or `error`. |
| `shutdown_grace` | duration | `30s` | How long musterd waits after SIGTERM for busy sessions to end. See [upgrade](operations/upgrade.md). |
| `github` | object | required | See [github](#github). |
| `battery` | object | required | See [battery](#battery). |
| `flintlock` | object | | See [flintlock](#flintlock). |
| `defaults` | object | | See [defaults](#defaults). |
| `pools` | array | required | At least one pool. See [pools](#pools). |

At least one intake must be on: set `listen`, or set `github.poll.enabled` to `true`, or both.

## listen

The webhook intake. When `listen` is absent, musterd does not accept webhook deliveries.

| Field | Type | Default | Notes |
| --- | --- | --- | --- |
| `listen.addr` | host:port | `:8443` | Serves only `POST /webhook`. |
| `listen.tls` | [server TLS](#tls-blocks) | none | Without it, the listener serves plain HTTP. |

GitHub must reach the listener over HTTPS (INTAKE-8). Either set `listen.tls`, or put a
TLS terminator in front of musterd, such as a reverse proxy or a load balancer. With a
terminator, keep musterd's plain HTTP port off the public network. The proxy must forward
`POST /webhook` with the body and the `X-Hub-Signature-256`, `X-GitHub-Event` and
`X-GitHub-Delivery` headers unchanged, because musterd checks the signature of the body.

## api

The gRPC API that `musterctl` uses. The API arrives with
[#23](https://github.com/liquidmetal-dev/muster/issues/23); until then, musterd checks this
block but does not serve it.

| Field | Type | Default | Notes |
| --- | --- | --- | --- |
| `api.addr` | host:port | `:9443` | |
| `api.tls` | [server TLS](#tls-blocks) | required | For plaintext, set `{"insecure": true}`. |

## github

| Field | Type | Default | Notes |
| --- | --- | --- | --- |
| `github.api_base_url` | URL | `https://api.github.com` | See [GitHub Enterprise Server](#github-enterprise-server). |
| `github.app` | object | none | The GitHub App credential. |
| `github.app.app_id` | integer | required | |
| `github.app.installation_id` | integer | required | |
| `github.app.private_key_file` | path | | The App private key, an RSA key in PEM (PKCS#1 or PKCS#8). |
| `github.app.private_key_env` | env name | | The same key, from an environment variable. |
| `github.token_file` | path | | A personal access token. See [personal access token](#personal-access-token). |
| `github.token_env` | env name | | The same token, from an environment variable. |
| `github.webhook_secret_file` | path | | The webhook secret. One of the two is required when `listen` is set. |
| `github.webhook_secret_env` | env name | | |
| `github.poll` | object | | The polling intake. |
| `github.poll.enabled` | bool | `false` | |
| `github.poll.interval` | duration | `1m` | At least `10s`. |
| `github.poll.repos` | array of `owner/name` | required when enabled | Each repository must be inside the scope of a pool. No duplicates. |

Set exactly one credential: `github.app`, or one of `github.token_file` and
`github.token_env`. For `github.app`, set one of `private_key_file` and `private_key_env`.

One App installation belongs to one organization or user. So with `github.app`, every pool
scope and every polled repository must have one owner. To serve two owners, run two musterd
instances with different [namespaces](#battery).

### GitHub App permissions

The App needs these permissions (GH-6). Give it the scope's row and the common rows.

| Permission | Access | Pool scope | Used for |
| --- | --- | --- | --- |
| Repository: Administration | Read and write | repository | List, create (just-in-time) and delete the repository's runners. |
| Organization: Self-hosted runners | Read and write | organization | List, create (just-in-time) and delete the organization's runners; find `runner_group`. |
| Repository: Actions | Read | both | Polling, and the checks for the end of a job: list workflow runs and their jobs. |
| Repository: Metadata | Read | both | GitHub gives it to every App. |

Subscribe the App to the **Workflow job** event (`workflow_job`) when you use the webhook
intake. Set the webhook URL to `https://<host>:<port>/webhook`, where the port is the one in
`listen.addr` or the one of your TLS terminator, and set the webhook secret to the
value in `github.webhook_secret_file`.

The permissions come from GitHub's
[permissions for GitHub Apps](https://docs.github.com/en/rest/authentication/permissions-required-for-github-apps)
for the endpoints musterd calls:
[self-hosted runners](https://docs.github.com/en/rest/actions/self-hosted-runners),
[runner groups](https://docs.github.com/en/rest/actions/self-hosted-runner-groups),
[workflow runs](https://docs.github.com/en/rest/actions/workflow-runs) and
[workflow jobs](https://docs.github.com/en/rest/actions/workflow-jobs).

### Personal access token

Instead of an App, musterd can use a personal access token through `github.token_file` or
`github.token_env`. The token's user must be an admin of each pool scope: of the repository for
a repository pool, of the organization for an organization pool.

| Token | Repository pool | Organization pool |
| --- | --- | --- |
| Classic | `repo` | `admin:org`, and `repo` when the organization's repositories are private |
| Fine-grained | Repository: Administration (read and write), Actions (read) | Organization: Self-hosted runners (read and write); Repository: Actions (read) on the repositories that run jobs |

The webhook intake does not need the token: the App, or a repository or organization webhook,
sends the deliveries. A repository or organization webhook needs the **Workflow jobs** event,
the URL `https://<host>:<port>/webhook`, content type `application/json` and the webhook secret.

### GitHub Enterprise Server

Set `github.api_base_url` to the address of your GitHub Enterprise Server, for example
`https://ghes.example.com`. musterd adds `/api/v3` when the URL does not end with it, so
`https://ghes.example.com/api/v3` also works. Create the App on the GHES instance, and make
sure that GHES can reach the webhook URL.

## battery

| Field | Type | Default | Notes |
| --- | --- | --- | --- |
| `battery.addr` | host:port | required | The poolmgrd gRPC address. |
| `battery.namespace` | DNS label | `muster` | The [namespace](../GLOSSARY.md#pools) of this instance. Two instances that share a battery must use different namespaces. See [upgrade](operations/upgrade.md#two-instances). |
| `battery.tls` | [client TLS](#tls-blocks) | TLS with the system CAs | |
| `battery.token_file` | path | | poolmgrd's `basic_auth_token`. Set at most one of `token_file` and `token_env`. |
| `battery.token_env` | env name | | The same token, from an environment variable. |

Set the token when poolmgrd sets `basic_auth_token`. musterd sends the token on each call to
poolmgrd as `authorization: basic <base64(token)>`, and removes the whitespace around it first.
Use TLS to protect the token. With `battery.tls.insecure`, the token goes in cleartext and
musterd logs a warning at startup.

## flintlock

musterd connects to each flintlock host to start the runner with flintlock's exec API.

| Field | Type | Default | Notes |
| --- | --- | --- | --- |
| `flintlock.tls` | [client TLS](#tls-blocks) | TLS with the system CAs | Used for every host without its own entry. |
| `flintlock.hosts` | map | none | The key is the host name in battery. |
| `flintlock.hosts.<name>.tls` | [client TLS](#tls-blocks) | | Replaces `flintlock.tls` for this host. |

A host name here and in `pools[].flintlock_hosts` must be the same as the host's name in
battery. See [hosts](operations/hosts.md).

## TLS blocks

A client TLS block (`battery.tls`, `flintlock.tls`, `flintlock.hosts.<name>.tls`):

| Field | Notes |
| --- | --- |
| `ca_file` | A PEM bundle that signs the server certificate. Empty uses the system CAs. |
| `cert_file` | A client certificate, for mutual TLS. Needs `key_file` or `key_env`. |
| `key_file` | The PEM private key of `cert_file`. |
| `key_env` | The same key, from an environment variable. |
| `insecure` | `true` selects plaintext. |

A server TLS block (`listen.tls`, `api.tls`):

| Field | Notes |
| --- | --- |
| `cert_file` | The server certificate. Required unless `insecure` is set. |
| `key_file` | The PEM private key of `cert_file`. |
| `key_env` | The same key, from an environment variable. |
| `client_ca_file` | A PEM bundle. When set, clients must show a certificate that it signs (mutual TLS). |
| `insecure` | `true` selects plaintext. |

When `insecure` is set, it must be the only field in the block.

## defaults

Session timers. Each must be a positive duration.

| Field | Type | Default | Notes |
| --- | --- | --- | --- |
| `defaults.claim_deadline` | duration | `10m` | How long a session tries to claim a VM while battery has none free. Then the session fails. |
| `defaults.start_timeout` | duration | `2m` | How long the runner can take to come online. |
| `defaults.idle_timeout` | duration | `5m` | How long a runner can be online without a job before its session ends. |
| `defaults.max_session_duration` | duration | `6h` | The longest a session can run. A pool can set its own. |
| `defaults.heartbeat_interval` | duration | `30s` | The heartbeat interval of each battery pool. musterd sends a lease heartbeat at least twice in each interval. |
| `defaults.heartbeat_expiry_threshold` | duration | `1m30s` | How long battery keeps a lease without a heartbeat. Must be greater than `defaults.heartbeat_interval`. |

## pools

Each entry is one [pool](../GLOSSARY.md#pools). At startup, musterd creates or updates the
battery pool for each entry in its namespace. A pool in the namespace that this list does not
name is an orphan pool: musterd logs it and keeps it. musterd deletes an orphan pool only when
it starts with the `-prune-pools` flag, and the flag deletes every orphan pool in the
namespace. A pool with leased VMs stays until a later start.
[Removing a pool](operations/upgrade.md#removing-a-pool-or-changing-its-scope) gives the
procedure.

| Field | Type | Default | Notes |
| --- | --- | --- | --- |
| `pools[].name` | DNS label | required | Unique. |
| `pools[].scope` | object | required | Set exactly one of `org` and `repo`. |
| `pools[].scope.org` | string | | An organization. |
| `pools[].scope.repo` | `owner/name` | | A repository. |
| `pools[].scope.runner_group` | string | the default group | Only with `org`. The name of the organization's runner group for this pool's runners. |
| `pools[].labels` | array of strings | required | Must include `self-hosted`. No white space, no commas, no duplicates. |
| `pools[].size` | integer | required | The number of warm VMs. At least 1. |
| `pools[].max_concurrent` | integer | `2 × size` | The most sessions of this pool at one time. |
| `pools[].max_session_duration` | duration | `defaults.max_session_duration` | |
| `pools[].work_folder` | relative path | `_work` | The runner's work folder, inside the runner directory. |
| `pools[].flintlock_hosts` | array of strings | required | The battery host names for this pool's VMs. No duplicates. |
| `pools[].create_commands` | array of strings | none | Battery runs these commands once in each new VM, after it boots. |
| `pools[].replenishment_strategy` | object | none | How battery refills the pool. |
| `pools[].replenishment_strategy.type` | string | `immediate_on_lease` | `immediate_on_lease`, `min_size_threshold` or `replace_on_delete`. |
| `pools[].replenishment_strategy.min_size` | integer | | Required for `min_size_threshold`, from 1 to `size`. Not allowed with the other types. |
| `pools[].microvm_template` | object | required | A flintlock `MicroVMSpec` in protojson. |

### Job matching

A job goes to the first pool, in the order of this list, whose labels include every label of
the job's `runs-on` (the case of the letters does not matter) and whose scope includes the
job's repository. musterd ignores a job that matches no pool. So a pool with the labels
`self-hosted`, `linux` and `muster` gets the jobs with `runs-on: [self-hosted, muster]`.

### microvm_template

- Do not set `id`, `namespace`, `uid` or the timestamps. muster and battery set them.
- Set `kernel` and `rootVolume` to a kernel and a [runner image](image-contract.md).
- Set `rootVolume.sizeInMb` to know how much thin-pool space each VM can use. See
  [capacity](operations/capacity.md).
- Do not set a `guestMac` or an `address` on an interface when the pool can hold more than
  one VM: every VM would get the same value.

### Runner names

The runner name of a session is `muster-<namespace>-<pool>-<session id>`, and the session id
has 12 characters. GitHub allows 64 characters, so the pool name and `battery.namespace`
together can have at most 43 characters.
