# Capacity: image pulls and thin-pool size

Each muster VM boots from a [runner image](../image-contract.md) and a kernel that flintlock
pulls through containerd. Each VM also takes space in the host's devmapper thin-pool. This
page helps you plan both.

## Image pulls

- flintlock pulls the kernel and the runner image on a host the first time that a VM on that
  host needs them. The first warm VM of a pool can take some minutes to boot. Later VMs use
  the image that is already on the host.
- To pull the images before you start musterd, run these commands on each flintlock host.
  flintlock uses the containerd namespace `flintlock`, the `devmapper` snapshotter for
  volumes and the `native` snapshotter for kernels:

  ```sh
  sudo ctr -n flintlock image pull --snapshotter devmapper ghcr.io/liquidmetal-dev/muster-runner:minimal-latest
  sudo ctr -n flintlock image pull --snapshotter native ghcr.io/liquidmetal-dev/firecracker-kernel:6.1-20260929-1349
  ```

- Use a fixed tag or a digest in `microvm_template`. With a moving tag such as
  `minimal-latest`, different hosts can boot different versions of the image.
- Registry credentials are set in containerd on each host, not in muster.

## Root volume size

`microvm_template.rootVolume.sizeInMb` sets the size of each VM's root volume. When it is not
set, the volume has the size of the image. Set it, so that jobs have space to write and so that
you can calculate the thin-pool size.

## Thin-pool size

Each VM, warm or leased, takes a thin snapshot of the runner image. The snapshot grows as the
VM writes, up to the root volume size.

With the `immediate_on_lease` strategy (the default), battery keeps `size` VMs warm, however
many VMs are leased. So one pool can have up to `size + max_concurrent` VMs at one time.

For each flintlock host:

```text
thin-pool size >= image size
                + sum over the pools on the host of (size + max_concurrent) x sizeInMb
                + 20% headroom
```

When a pool uses more than one host, battery decides where each VM goes. Dividing the pool's
VMs by the number of hosts gives only an estimate; plan each host for more than its share.

Monitor the data and metadata use of the thin-pool, for example with `sudo dmsetup status`.
When the thin-pool is full, flintlock cannot make new VMs, and sessions fail to claim a VM.
flintlock's [containerd guide](https://github.com/liquidmetal-dev/flintlock/blob/main/userdocs/docs/getting-started/containerd.md)
shows how to make the thin-pool.

## CPU and memory

Use the same count for CPU and memory. For each host, the sum over its pools of
`(size + max_concurrent) x vcpu` and of `(size + max_concurrent) x memoryInMb` must fit the
host, with space left for flintlockd, containerd and the host itself.
