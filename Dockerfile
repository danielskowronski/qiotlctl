# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.27.2@sha256:820153096fda4de6ac1b7194d12e416b7dd8f4e9a33d17e0345b111b945162d8 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
RUN --mount=type=cache,target=/go/pkg/mod --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -buildvcs=false -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/qiotld ./cmd/qiotld && \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -buildvcs=false -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/qiotlctl ./cmd/qiotlctl
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab
ARG VERSION=dev
ARG REVISION=unknown
LABEL org.opencontainers.image.title="Qingping IoT Control" \
      org.opencontainers.image.description="Local MQTT observation, commands and durable exports for Qingping devices" \
      org.opencontainers.image.source="https://github.com/danielskowronski/qiotlctl" \
      org.opencontainers.image.url="https://github.com/danielskowronski/qiotlctl" \
      org.opencontainers.image.version=$VERSION \
      org.opencontainers.image.revision=$REVISION \
      org.opencontainers.image.licenses="BSD-3-Clause"
COPY --from=build /out/qiotld /out/qiotlctl /usr/local/bin/
COPY LICENSE /usr/share/licenses/qiotlctl/LICENSE
COPY third_party/ /usr/share/licenses/qiotlctl/third_party/
COPY --from=build /usr/local/go/LICENSE /usr/share/licenses/qiotlctl/Go-LICENSE
USER 65532:65532
EXPOSE 9090
ENTRYPOINT ["/usr/local/bin/qiotld"]
