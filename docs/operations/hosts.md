# Registering flintlock hosts

Battery keeps the list of flintlock hosts. musterd reads that list, but it never adds, updates,
cordons or removes a host (POOL-3). The operator registers each host in battery before musterd
starts.

## What each host needs

- flintlockd v0.15.2 or later, started with `--enable-exec-api` (POOL-4). musterd starts each
  runner through flintlock's exec API.
- A name in battery that is the same as the name in muster's configuration: in
  `pools[].flintlock_hosts`, and in `flintlock.hosts` when the host has its own TLS settings.
- Space in the devmapper thin-pool, CPU and memory for the pools that use it. See
  [capacity](capacity.md).

## Register a host

Use `poolmgrctl`:

```sh
poolmgrctl host add host-a --address host-a.example.com:9090
```

Or call `HostAdmin.AddHost` directly:

```sh
grpcurl -d '{"host": {"name": "host-a", "address": "host-a.example.com:9090"}}' \
  poolmgrd.example.com:8443 poolmgr.v1alpha1.HostAdmin/AddHost
```

Battery connects to the host and records its flintlock version. To see the host, run
`poolmgrctl host get host-a`.

## When a host is missing

When a pool names a host that battery does not know, musterd does not start. It reports each
missing host:

```text
battery hosts: pools[ubuntu-small]: host "host-a" is not registered in battery; add it with HostAdmin.AddHost
```

Register the host, then start musterd again.
