FROM ghcr.io/liquidmetal-dev/muster-runner:minimal-latest AS build

COPY --chmod=0755 --chown=1000:1000 run.sh /opt/actions-runner/run.sh

FROM scratch
COPY --from=build / /
LABEL org.opencontainers.image.source=https://github.com/liquidmetal-dev/muster
CMD ["/sbin/init"]
