FROM ubuntu:24.04 AS build

ARG TARGETARCH
ARG GUEST_AGENT_VERSION=0.5.0
ARG RUNNER_VERSION=2.338.2
ARG RUNNER_SHA256_AMD64=baebf204307bac6193480852732f0aba2ae97b6c5597c244f4d5558390147c40
ARG RUNNER_SHA256_ARM64=670adb2f268fe21dcbb58b3db0e0aa9d5b3bddfe2c651481336e561ff86de945

ENV DEBIAN_FRONTEND=noninteractive
SHELL ["/bin/bash", "-euo", "pipefail", "-c"]

RUN userdel -r ubuntu

RUN apt-get update && \
    apt-get install -y --no-install-recommends \
      systemd systemd-sysv systemd-resolved dbus udev iproute2 sudo gnupg \
      git curl jq tar zstd build-essential ca-certificates && \
    rm -rf /var/lib/apt/lists/*

RUN install -d -m 0755 /usr/share/keyrings && \
    curl -fsSL https://liquidmetal-dev.github.io/apt-repo/liquidmetal-archive-keyring.asc \
      | gpg --dearmor -o /usr/share/keyrings/liquidmetal-archive-keyring.gpg && \
    echo "deb [signed-by=/usr/share/keyrings/liquidmetal-archive-keyring.gpg] https://liquidmetal-dev.github.io/apt-repo stable main" \
      > /etc/apt/sources.list.d/liquidmetal.list && \
    curl -fsSL https://download.docker.com/linux/ubuntu/gpg \
      | gpg --dearmor -o /usr/share/keyrings/docker-archive-keyring.gpg && \
    echo "deb [arch=${TARGETARCH} signed-by=/usr/share/keyrings/docker-archive-keyring.gpg] https://download.docker.com/linux/ubuntu $(. /etc/os-release; echo "$VERSION_CODENAME") stable" \
      > /etc/apt/sources.list.d/docker.list && \
    apt-get update && \
    apt-get install -y --no-install-recommends \
      "guest-agent=${GUEST_AGENT_VERSION}" \
      docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin && \
    rm -rf /var/lib/apt/lists/*

RUN case "${TARGETARCH}" in \
      amd64) arch=x64; sum="${RUNNER_SHA256_AMD64}" ;; \
      arm64) arch=arm64; sum="${RUNNER_SHA256_ARM64}" ;; \
      *) echo "unsupported TARGETARCH ${TARGETARCH}" >&2; exit 1 ;; \
    esac && \
    tarball="actions-runner-linux-${arch}-${RUNNER_VERSION}.tar.gz" && \
    curl -fsSLo "/tmp/${tarball}" "https://github.com/actions/runner/releases/download/v${RUNNER_VERSION}/${tarball}" && \
    echo "${sum}  /tmp/${tarball}" | sha256sum -c - && \
    install -d /opt/actions-runner && \
    tar -xzf "/tmp/${tarball}" -C /opt/actions-runner && \
    rm "/tmp/${tarball}" && \
    apt-get update && \
    /opt/actions-runner/bin/installdependencies.sh && \
    rm -rf /var/lib/apt/lists/*

COPY 10-dhcp.network /etc/systemd/network/10-dhcp.network
COPY --chmod=0440 sudoers-runner /etc/sudoers.d/runner

RUN groupadd --gid 1000 runner && \
    useradd --uid 1000 --gid 1000 --groups docker --create-home --shell /bin/bash runner && \
    chown -R runner:runner /opt/actions-runner && \
    systemctl enable guest-agent docker containerd systemd-networkd systemd-resolved && \
    truncate -s 0 /etc/machine-id

COPY rootfs/ /

FROM scratch
COPY --from=build / /
LABEL org.opencontainers.image.source=https://github.com/liquidmetal-dev/muster
CMD ["/sbin/init"]
