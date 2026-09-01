# syntax=docker/dockerfile:1.7@sha256:a57df69d0ea827fb7266491f2813635de6f17269be881f696fbfdf2d83dda33e

FROM node:24-bookworm@sha256:392e1e23f34da768d8d1f4e502b64f200d3be3465934d4b7930f57d7e2fc1989 AS node

# A caller may override this empty stage with a named build context containing
# basemap.pmtiles. The generator verifies the pinned digest before accepting it.
FROM scratch AS mapassetseed

FROM golang:1.25.14-bookworm@sha256:3b4a11519ad929d1e1d261a12cff056f0c85b735253d7d861346b9c6f8b36437 AS go-deps
WORKDIR /src

COPY go.mod go.sum ./
COPY pkg/apigen/go.mod pkg/apigen/go.sum ./pkg/apigen/
RUN go mod download

FROM go-deps AS sourcegen

COPY --from=node /usr/local/bin/node /usr/local/bin/node
COPY --from=node /usr/local/lib/node_modules /usr/local/lib/node_modules
RUN ln -sf ../lib/node_modules/npm/bin/npm-cli.js /usr/local/bin/npm && \
    ln -sf ../lib/node_modules/npm/bin/npx-cli.js /usr/local/bin/npx

COPY . .

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,id=leapview-go-mod,target=/go/pkg/mod,from=go-deps,source=/go/pkg/mod,sharing=locked \
    ./scripts/generate_build_sources.sh && \
    go run ./internal/app/tools/clidocgen && \
    go run ./internal/app/tools/schemadocgen && \
    go run ./internal/app/tools/openapidocgen && \
    go run ./internal/app/tools/docsitegen

# Keep the large, network-backed map extraction separate so a transient remote
# failure can be retried without repeating deterministic source generation.
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,id=leapview-go-mod,target=/go/pkg/mod,from=go-deps,source=/go/pkg/mod,sharing=locked \
    --mount=type=bind,from=mapassetseed,source=/,target=/mapasset-seed,ro \
    if [ -f /mapasset-seed/basemap.pmtiles ]; then \
      go run ./internal/app/tools/mapassets --out .data/map-assets --seed-archive /mapasset-seed/basemap.pmtiles; \
    else \
      go run ./internal/app/tools/mapassets --out .data/map-assets; \
    fi

FROM oven/bun:1.4.0@sha256:5ff609364c049b54eb0ff560ec96319729a972078ef2c755d758f0c6ef89c2d6 AS web
WORKDIR /src

COPY --from=go-deps /usr/local/go/bin/gofmt /usr/local/bin/gofmt
COPY package.json bun.lock tsconfig.json ./
COPY scripts ./scripts
COPY static ./static
COPY web ./web
COPY --from=sourcegen /src/api/gen ./api/gen
COPY --from=sourcegen /src/api/visualization ./api/visualization
COPY --from=sourcegen /src/web/generated ./web/generated

RUN bun install --frozen-lockfile --no-cache
RUN mkdir -p internal/dashboard/appearance && \
    bun scripts/generate_lucide_icon_catalog.ts && \
    bun scripts/generate_visualization_validator.ts && \
    bun run build

FROM go-deps AS build

ARG BUILD_VERSION=development
ARG BUILD_REVISION=unknown
ARG BUILD_TIME=unknown
ARG BUILD_DIRTY=true
ARG BUILD_RELEASE=false

COPY . .
COPY --from=sourcegen /src/api/gen ./api/gen
COPY --from=sourcegen /src/internal/access/api/gen ./internal/access/api/gen
COPY --from=sourcegen /src/internal/agent/api/gen ./internal/agent/api/gen
COPY --from=sourcegen /src/internal/analytics/api/gen ./internal/analytics/api/gen
COPY --from=sourcegen /src/internal/dashboard/api/gen ./internal/dashboard/api/gen
COPY --from=sourcegen /src/internal/deployment/api/gen ./internal/deployment/api/gen
COPY --from=sourcegen /src/internal/manageddata/api/gen ./internal/manageddata/api/gen
COPY --from=sourcegen /src/internal/app/api/aggregate ./internal/app/api/aggregate
COPY --from=sourcegen /src/internal/app/api/gen ./internal/app/api/gen
COPY --from=sourcegen /src/internal/platform/http/api/gen ./internal/platform/http/api/gen
COPY --from=sourcegen /src/internal/project/api/gen ./internal/project/api/gen
COPY --from=sourcegen /src/internal/refresh/api/gen ./internal/refresh/api/gen
COPY --from=sourcegen /src/internal/release/api/gen ./internal/release/api/gen
COPY --from=sourcegen /src/internal/app/cli/gen ./internal/app/cli/gen
COPY --from=sourcegen /src/internal/app/config/config_gen.go ./internal/app/config/config_gen.go
COPY --from=sourcegen /src/internal/app/config/spec/names_gen.go ./internal/app/config/spec/names_gen.go
COPY --from=sourcegen /src/internal/access/internal/db ./internal/access/internal/db
COPY --from=sourcegen /src/internal/admin/internal/db ./internal/admin/internal/db
COPY --from=sourcegen /src/internal/agent/internal/db ./internal/agent/internal/db
COPY --from=sourcegen /src/internal/analytics/internal/db ./internal/analytics/internal/db
COPY --from=sourcegen /src/internal/dashboard/internal/db ./internal/dashboard/internal/db
COPY --from=sourcegen /src/internal/deployment/internal/db ./internal/deployment/internal/db
COPY --from=sourcegen /src/internal/manageddata/internal/db ./internal/manageddata/internal/db
COPY --from=sourcegen /src/internal/refresh/internal/db ./internal/refresh/internal/db
COPY --from=sourcegen /src/internal/release/internal/db ./internal/release/internal/db
COPY --from=sourcegen /src/internal/servingstate/internal/db ./internal/servingstate/internal/db
COPY --from=sourcegen /src/internal/project/internal/db ./internal/project/internal/db
COPY --from=sourcegen /src/internal/platform/db/db.go ./internal/platform/db/db.go
COPY --from=sourcegen /src/internal/platform/db/models.go ./internal/platform/db/models.go
COPY --from=sourcegen /src/internal/platform/db/*.sql.go ./internal/platform/db/
COPY --from=sourcegen /src/internal/platform/http/cursorsigning/sqlite/cursordb ./internal/platform/http/cursorsigning/sqlite/cursordb
COPY --from=sourcegen /src/internal/platform/http/idempotency/sqlite/idempotencydb ./internal/platform/http/idempotency/sqlite/idempotencydb
COPY --from=sourcegen /src/internal/platform/jobs/sqlite/jobdb ./internal/platform/jobs/sqlite/jobdb
COPY --from=sourcegen /src/internal/access/ui/signals/models.gen.go ./internal/access/ui/signals/models.gen.go
COPY --from=sourcegen /src/internal/admin/ui/signals/models.gen.go ./internal/admin/ui/signals/models.gen.go
COPY --from=sourcegen /src/internal/agent/ui/signals/models.gen.go ./internal/agent/ui/signals/models.gen.go
COPY --from=sourcegen /src/internal/dashboard/ui/signals/models.gen.go ./internal/dashboard/ui/signals/models.gen.go
COPY --from=web /src/internal/dashboard/appearance/icons_gen.go ./internal/dashboard/appearance/icons_gen.go
COPY --from=sourcegen /src/docs ./docs
COPY --from=sourcegen /src/schemas ./schemas
COPY --from=sourcegen /src/web/generated ./web/generated
COPY --from=web /src/static ./static

RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,id=leapview-go-mod,target=/go/pkg/mod,from=go-deps,source=/go/pkg/mod,sharing=locked \
    BUILD_LDFLAGS="-s -w \
      -X github.com/flidai/leapview/internal/platform/buildinfo.version=${BUILD_VERSION} \
      -X github.com/flidai/leapview/internal/platform/buildinfo.revision=${BUILD_REVISION} \
      -X github.com/flidai/leapview/internal/platform/buildinfo.buildTime=${BUILD_TIME} \
      -X github.com/flidai/leapview/internal/platform/buildinfo.dirty=${BUILD_DIRTY} \
      -X github.com/flidai/leapview/internal/platform/buildinfo.release=${BUILD_RELEASE}" && \
    CGO_ENABLED=1 go build -tags=duckdb_arrow -trimpath -ldflags="$BUILD_LDFLAGS" -o /out/leapview ./cmd/leapview && \
    CGO_ENABLED=1 go build -tags=duckdb_arrow -trimpath -ldflags="$BUILD_LDFLAGS" -o /out/leapviewctl ./cmd/leapviewctl

# The production image carries a complete, target-native, offline extension
# supply. This stage performs the only upstream acquisition during packaging;
# the tool installs the exact pinned core artifacts, asks DuckDB to LOAD every
# absolute file with automatic install/load disabled, and emits a digested
# manifest consumed by the runtime image.
FROM build AS extension-supply
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,id=leapview-go-mod,target=/go/pkg/mod,from=go-deps,source=/go/pkg/mod,sharing=locked \
    go run ./internal/app/tools/extensionsupply --out /out/extension-supply

FROM debian:bookworm-slim@sha256:60eac759739651111db372c07be67863818726f754804b8707c90979bda511df AS runtime

ARG BUILD_VERSION=development
ARG BUILD_REVISION=unknown
ARG BUILD_TIME=unknown
ARG BUILD_DIRTY=true
ARG BUILD_RELEASE=false

LABEL org.opencontainers.image.title="LeapView" \
      org.opencontainers.image.description="LeapView business intelligence server" \
      org.opencontainers.image.source="https://github.com/flidai/leapview" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.version="$BUILD_VERSION" \
      org.opencontainers.image.revision="$BUILD_REVISION" \
      org.opencontainers.image.created="$BUILD_TIME" \
      dev.leapview.build.dirty="$BUILD_DIRTY" \
      dev.leapview.build.release="$BUILD_RELEASE"

# The pinned Go builder supplies the bootstrap CA bundle. APT then resolves
# every direct and transitive runtime package from one immutable Debian
# snapshot and verifies the signed repository metadata and package hashes.
COPY --from=go-deps /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY deploy/container/debian-bookworm.sources /etc/apt/sources.list.d/debian.sources

RUN apt-get update && \
    apt-get install -y --no-install-recommends ca-certificates libstdc++6 tzdata && \
    rm -rf /var/lib/apt/lists/*

RUN groupadd --system leapview && \
    useradd --system --gid leapview --home-dir /var/lib/leapview --shell /usr/sbin/nologin leapview

WORKDIR /app

COPY --from=build /out/leapview /usr/local/bin/leapview
COPY --from=build /out/leapviewctl /usr/local/libexec/leapviewctl
COPY --from=build /out/leapviewctl /usr/local/share/leapview/deployment/leapviewctl
COPY --from=extension-supply /out/extension-supply /usr/local/share/leapview/extensions
COPY deploy/compose/compose.yaml deploy/compose/compose.https.yaml deploy/compose/Caddyfile deploy/compose/deployment.env.example deploy/compose/leapview.env.example deploy/compose/README.md deploy/compose/QUALIFICATION.md /usr/local/share/leapview/deployment/
COPY deploy/compose/qualification /usr/local/share/leapview/deployment/qualification
COPY internal/platform/compatibility/release-transition-policy.json /usr/local/share/leapview/deployment/release-transition-policy.json
COPY deploy/host/files/ /usr/local/share/leapview/deployment/
COPY --from=web /src/static ./static
COPY --from=build /src/schemas ./schemas
COPY --from=sourcegen /src/.data/map-assets ./.data/map-assets
COPY dashboards ./dashboards
COPY evaluation ./evaluation

RUN chmod 0500 /usr/local/share/leapview/deployment/leapviewctl \
      /usr/local/share/leapview/deployment/leapviewctl-wrapper \
      /usr/local/share/leapview/deployment/leapview-backup-hook && \
    find /usr/local/share/leapview/extensions -type d -exec chmod 0555 {} + && \
    find /usr/local/share/leapview/extensions -type f -exec chmod 0444 {} + && \
    chmod 0400 /usr/local/share/leapview/deployment/compose.yaml \
      /usr/local/share/leapview/deployment/compose.https.yaml \
      /usr/local/share/leapview/deployment/Caddyfile \
      /usr/local/share/leapview/deployment/deployment.env.example \
      /usr/local/share/leapview/deployment/leapview.env.example \
      /usr/local/share/leapview/deployment/README.md \
      /usr/local/share/leapview/deployment/QUALIFICATION.md \
      /usr/local/share/leapview/deployment/qualification/* \
      /usr/local/share/leapview/deployment/leapview-backup.service \
      /usr/local/share/leapview/deployment/leapview-backup.timer \
      /usr/local/share/leapview/deployment/leapview-backup-maintenance.service \
      /usr/local/share/leapview/deployment/leapview-backup-maintenance.timer \
      /usr/local/share/leapview/deployment/leapview-recovery-qualification.service \
      /usr/local/share/leapview/deployment/leapview-recovery-qualification.timer && \
    mkdir -p /var/lib/leapview && \
    chown -R leapview:leapview /var/lib/leapview /app

USER leapview

ENV LEAPVIEW_ADDR=:8080 \
    LEAPVIEW_ENVIRONMENT=prod \
    LEAPVIEW_HOME=/var/lib/leapview/home \
    LEAPVIEW_MAP_ASSET_DIR=/app/.data/map-assets \
    LEAPVIEW_MANAGED_DATA_DIR=/var/lib/leapview/home/managed-data \
    LEAPVIEW_DUCKDB_EXTENSION_SUPPLY_PATH=/usr/local/share/leapview/extensions/extension-supply.json \
    LEAPVIEW_PRODUCTION=1

EXPOSE 8080
VOLUME ["/var/lib/leapview"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 CMD ["leapview", "healthcheck"]

ENTRYPOINT ["leapview"]
CMD ["serve", "--production"]
