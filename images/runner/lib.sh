#!/usr/bin/env bash
# Values and functions that the runner image scripts share. Source it; it runs nothing.

# shellcheck disable=SC2034 # used by the scripts that source this file
default_image=ghcr.io/liquidmetal-dev/muster-runner:minimal-latest

# dockerfile_runner_version <Dockerfile>
dockerfile_runner_version() {
  sed -n 's/^ARG RUNNER_VERSION=//p' "$1"
}

# image_layer <image> <work dir>: saves the image into the work dir and prints the path of
# its layer tarball. Fails when the image has more than one layer.
image_layer() {
  local image="$1" work="$2" layer
  docker save "$image" -o "$work/image.tar"
  tar -xf "$work/image.tar" -C "$work" manifest.json
  if [ "$(jq '.[0].Layers | length' "$work/manifest.json")" != 1 ]; then
    echo "$image must have exactly one layer" >&2
    return 1
  fi
  layer="$(jq -r '.[0].Layers[0]' "$work/manifest.json")"
  tar -xf "$work/image.tar" -C "$work" "$layer"
  rm "$work/image.tar"
  echo "$work/$layer"
}
