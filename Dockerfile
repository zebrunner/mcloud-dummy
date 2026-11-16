# The program is built by the Go image of the build platform for the target one, the image has no other package
FROM --platform=${BUILDPLATFORM} golang:1.27.3-alpine@sha256:fa3d596898d9f126af7c01d14ee7597bab3f39cccf392ef36b69fe77cd681f18 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags "-s -w" -o /out/mcloud-dummy ./cmd/mcloud-dummy

FROM alpine:3.24.3@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a

# the defaults of the configuration, all of them are documented in README.md
ENV DUMMY_CONFIG=/opt/zebrunner/devices.yml \
    DUMMY_HOST="" \
    # empty: the control of the devices file, :8800 without it
    DUMMY_CONTROL="" \
    LOGGER_LEVEL=INFO

WORKDIR /opt/zebrunner/

COPY --from=build /out/mcloud-dummy /opt/zebrunner/mcloud-dummy
COPY examples/devices.yml /opt/zebrunner/devices.yml

ARG VERSION=dev
LABEL org.opencontainers.image.title="Zebrunner MCloud dummy devices" \
      org.opencontainers.image.description="Dummy Android and iOS devices for testing the Zebrunner MCloud device farm" \
      org.opencontainers.image.source="https://github.com/zebrunner/mcloud-dummy" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="${VERSION}"

EXPOSE 8800

ENTRYPOINT ["/opt/zebrunner/mcloud-dummy"]

HEALTHCHECK --interval=20s --timeout=5s --start-period=10s --retries=3 \
    CMD ["/opt/zebrunner/mcloud-dummy", "-healthcheck"]
