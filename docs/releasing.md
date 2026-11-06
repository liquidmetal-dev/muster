# Releasing muster

A release starts when a maintainer pushes a semver tag. The `Release` workflow
(`.github/workflows/release.yml`) runs goreleaser with the configuration in `.goreleaser.yaml`.

## What a tag publishes

A tag such as `v0.1.0` publishes these artefacts:

- `musterd` archives for linux/amd64 and linux/arm64. Each archive also holds the systemd unit,
  the sysusers file and the example configuration, for a manual install.
- `musterctl` archives for linux and darwin, on amd64 and arm64.
- `checksums.txt`, with the SHA-256 of each archive and package.
- The deb and rpm packages `musterd` and `musterctl`, for amd64 and arm64.
- The container image `ghcr.io/liquidmetal-dev/musterd` for linux/amd64 and linux/arm64.
- A changelog with the groups "Features", "Bug fixes" and "Other". The changelog does not
  show `docs`, `test`, `ci` and `chore` commits, or merge commits.

The image tags do not have the `v`: the tag `v0.1.0` gives `ghcr.io/liquidmetal-dev/musterd:0.1.0`
and `:latest`. A prerelease tag, such as `v0.1.0-rc.1`, does not move `:latest`. The GitHub
release for a prerelease tag is marked as a prerelease.

The release does not sign the artefacts and does not attach an SBOM.

## Make a release

1. Make sure that the `CI` workflow on `main` passed, including the `Release snapshot` job.
2. Tag the commit and push the tag:

   ```sh
   git tag -s v0.1.0 -m v0.1.0
   git push origin v0.1.0
   ```

3. Wait for the `Release` workflow. Its last step makes sure that the image has both
   platforms.
4. After the first release only, set the visibility of the `musterd` package on ghcr.io to
   public. GitHub makes a new package private.

## Do a dry run

The `Release snapshot` CI job does this on each pull request. To do it on your machine, you
need Docker with buildx:

```sh
mise exec -- goreleaser release --snapshot --clean
bash hack/release/package-smoke.sh ubuntu:24.04 dist
bash hack/release/package-smoke.sh fedora:45 dist
```

A snapshot does not push images. It loads them into Docker with the tags
`ghcr.io/liquidmetal-dev/musterd:<version>-amd64` and `-arm64`.

`package-smoke.sh` installs the packages in a container and examines the result:

- the `muster` user and the modes of `/etc/muster`
- the unit, with `systemd-analyze verify`
- the version that the two binaries print
- musterd as `muster`, with secret files that only the group can read
- a reinstall, which must not change the configuration
