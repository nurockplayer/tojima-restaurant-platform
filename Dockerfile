# syntax=docker/dockerfile:1

# Stage 1: guest/staff SPA. Node and pnpm match web/package.json.
FROM node:24.15.0-bookworm-slim AS web
ENV COREPACK_ENABLE_DOWNLOAD_PROMPT=0
WORKDIR /src/web
RUN corepack enable
COPY web/package.json web/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile
COPY web/ ./
RUN pnpm build

# Stage 2: Go binaries (application server and one-shot migration runner).
# Go matches go.mod; CGO is off so the result runs on distroless static.
FROM golang:1.27.1-bookworm AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY migrations ./migrations
RUN CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/server ./cmd/server \
 && CGO_ENABLED=0 go build -trimpath -ldflags='-s -w' -o /out/migrate ./cmd/migrate

# Stage 3: minimal non-root runtime. No shell and no package manager, so the
# compose healthcheck runs /server -healthcheck instead of curl.
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/server /server
COPY --from=build /out/migrate /migrate
# Built SPA assets; documented at /app/web/dist in docs/ops/containers.md.
# cmd/server serves these assets with SPA navigation fallback.
COPY --from=web /src/web/dist /app/web/dist
ENV APP_ADDR=0.0.0.0:8080
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/server"]
