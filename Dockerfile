FROM gcr.io/distroless/static-debian12:nonroot
ARG TARGETPLATFORM
COPY $TARGETPLATFORM/musterd /usr/bin/musterd
ENTRYPOINT ["/usr/bin/musterd"]
