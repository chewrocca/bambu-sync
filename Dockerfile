# syntax=docker/dockerfile:1

FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
# CGO_ENABLED=0 is required: the runtime stage is distroless/static, which has
# no libc to dynamically link against.
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" \
    -o /out/bambu-sync ./cmd/bambu-sync

FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/bambu-sync /bambu-sync
EXPOSE 9110
# NUMERIC uid:gid, not the `nonroot` name. Kubernetes' runAsNonRoot check
# cannot verify a named user is non-root and refuses to start the container:
#   "image has non-numeric user (nonroot), cannot verify user is non-root"
# 65532 is distroless's nonroot user.
USER 65532:65532
ENTRYPOINT ["/bambu-sync"]
