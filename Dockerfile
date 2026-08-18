# Polyglot builds to a single static binary with the WebUI embedded.
# The result is one container, one process, one SQLite file.

# --- stage 1: build the WebUI ------------------------------------------------
FROM node:22-alpine AS web

# corepack ships with the image and pins the pnpm version from package.json.
RUN corepack enable

WORKDIR /web
COPY web/package.json web/pnpm-lock.yaml web/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile

COPY web/ ./
# vite build does not typecheck, so fail the image on a type or lint error
# rather than shipping a broken bundle.
RUN pnpm run typecheck && pnpm run lint && pnpm run build

# --- stage 2: build the binary ----------------------------------------------
FROM golang:1.26.6-alpine AS build

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .
# Overwrite the placeholder dist with the real bundle before go:embed runs.
COPY --from=web /web/dist ./web/dist

ARG VERSION=dev
ARG COMMIT=unknown
# CGO stays off: the SQLite driver is pure Go, so the binary is fully static.
RUN CGO_ENABLED=0 go build -trimpath \
    -ldflags "-s -w \
      -X github.com/qunqin24/polyglot/internal/version.Version=${VERSION} \
      -X github.com/qunqin24/polyglot/internal/version.Commit=${COMMIT}" \
    -o /out/polyglot ./cmd/polyglot

# --- stage 3: runtime --------------------------------------------------------
FROM alpine:3.21

RUN apk add --no-cache ca-certificates tzdata wget && \
    adduser -D -u 10001 polyglot && \
    mkdir -p /data && chown polyglot:polyglot /data

COPY --from=build /out/polyglot /usr/local/bin/polyglot

USER polyglot
WORKDIR /data
VOLUME ["/data"]
EXPOSE 3000

ENV LISTEN=:3000 \
    DATA_DIR=/data

HEALTHCHECK --interval=30s --timeout=5s --start-period=5s --retries=3 \
    CMD wget -qO- http://127.0.0.1:3000/health >/dev/null || exit 1

ENTRYPOINT ["/usr/local/bin/polyglot"]
