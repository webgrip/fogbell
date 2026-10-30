# syntax=docker/dockerfile:1
ARG REGISTRY_DOCKERHUB=docker.io
# renovate: datasource=docker depName=golang versioning=docker
ARG GO_IMAGE=${REGISTRY_DOCKERHUB}/library/golang:1.27.3-alpine@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb
# renovate: datasource=docker depName=gcr.io/distroless/static-debian12 versioning=docker
ARG RUNTIME_IMAGE=gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

FROM ${GO_IMAGE} AS build
ARG IMAGE_VERSION=dev
WORKDIR /src
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN go build -ldflags "-s -w -X main.version=${IMAGE_VERSION}" -o /out/fogbell ./cmd/fogbell

FROM ${RUNTIME_IMAGE} AS app
ARG IMAGE_CREATED=1970-01-01T00:00:00Z
ARG IMAGE_VERSION=dev
ARG IMAGE_REVISION=unknown
LABEL org.opencontainers.image.title="fogbell" \
      org.opencontainers.image.description="One Slack thread per Alertmanager incident, with buttons that act on it" \
      org.opencontainers.image.source="https://forgejo.webgrip.dev/webgrip/fogbell" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.created="${IMAGE_CREATED}" \
      org.opencontainers.image.version="${IMAGE_VERSION}" \
      org.opencontainers.image.revision="${IMAGE_REVISION}"
COPY --from=build /out/fogbell /fogbell
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/fogbell"]
