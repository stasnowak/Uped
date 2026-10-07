# syntax=docker/dockerfile:1
# uped as a tiny scratch image for linux/amd64 and linux/arm64. Go cross-compiles,
# so the build stage always runs on the build machine's platform (no QEMU).
#   docker buildx build --platform linux/amd64,linux/arm64 --build-arg VERSION=v0.1.0 .
FROM --platform=$BUILDPLATFORM golang:1.24-alpine AS build
ARG TARGETOS TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X main.version=$VERSION" -o /out/uped ./cmd/uped
# An empty /data owned by the runtime user: Docker copies it, with its owner,
# into a new named volume, so the volume is writable without a chown.
RUN mkdir /out/data

FROM scratch
COPY --from=build /out/uped /uped
COPY --from=build --chown=65532:65532 /out/data /data
USER 65532:65532
VOLUME /data
EXPOSE 8080
ENTRYPOINT ["/uped", "--data", "/data", "--listen", ":8080"]
