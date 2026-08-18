# --- build stage -------------------------------------------------------------
FROM --platform=$BUILDPLATFORM golang:1.26.6-alpine AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
ARG VERSION=0.0.0
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS="${TARGETOS:-linux}" GOARCH="${TARGETARCH}" \
    go build -trimpath -ldflags="-s -w -X main.version=${VERSION} -o /out/score2cloudrun ./cmd/score2cloudrun

# --- runtime stage -----------------------------------------------------------
FROM gcr.io/google.com/cloudsdktool/google-cloud-cli:580.0.0-alpine

RUN apk add --no-cache jq

COPY --from=builder /out/score2cloudrun /usr/local/bin/score2cloudrun
COPY scripts/entrypoint.sh /usr/local/bin/entrypoint.sh

USER 1000:1000

ENTRYPOINT ["/usr/local/bin/entrypoint.sh"]
