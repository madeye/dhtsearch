FROM node:22-bookworm-slim AS web-deps
WORKDIR /src/web

ENV NEXT_TELEMETRY_DISABLED=1

COPY web/package.json web/package-lock.json ./
RUN --mount=type=cache,target=/root/.npm npm ci

FROM web-deps AS web-builder
ARG NEXT_PUBLIC_API_BASE=http://127.0.0.1:8080
ENV NEXT_PUBLIC_API_BASE=${NEXT_PUBLIC_API_BASE}

COPY web/ ./
RUN npm run build

FROM golang:1.25.6-alpine AS server-builder
WORKDIR /src/server

COPY server/go.mod server/go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY server/ ./
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/dhtsearch-server ./cmd/server

FROM node:22-bookworm-slim AS runtime
WORKDIR /app

ENV NODE_ENV=production
ENV NEXT_TELEMETRY_DISABLED=1
ENV HOSTNAME=0.0.0.0
ENV PORT=3000
ENV HTTP_ADDR=:8080
ENV DB_PATH=/data/dhtsearch.db
ENV DHT_PORT=6881

RUN mkdir -p /app/server /app/web /data && chown -R node:node /app /data

COPY --from=server-builder --chown=node:node /out/dhtsearch-server /app/server/dhtsearch-server
COPY --from=web-builder --chown=node:node /src/web/.next/standalone/ /app/web/
COPY --from=web-builder --chown=node:node /src/web/.next/static /app/web/.next/static
COPY --from=web-builder --chown=node:node /src/web/public /app/web/public

RUN touch /app/.env && chown node:node /app/.env && \
    cat <<'EOF' >/usr/local/bin/start-dhtsearch
#!/bin/sh
set -eu

cleanup() {
  if [ "${web_pid:-}" ]; then
    kill "$web_pid" 2>/dev/null || true
    wait "$web_pid" 2>/dev/null || true
  fi
  if [ "${api_pid:-}" ]; then
    kill "$api_pid" 2>/dev/null || true
    wait "$api_pid" 2>/dev/null || true
  fi
}

trap cleanup INT TERM

/app/server/dhtsearch-server &
api_pid=$!

cd /app/web
node server.js &
web_pid=$!

status=0
while :; do
  if ! kill -0 "$api_pid" 2>/dev/null; then
    wait "$api_pid" || status=$?
    break
  fi
  if ! kill -0 "$web_pid" 2>/dev/null; then
    wait "$web_pid" || status=$?
    break
  fi
  sleep 1
done

cleanup
wait || true
exit "$status"
EOF

RUN chmod +x /usr/local/bin/start-dhtsearch

USER node

EXPOSE 3000 8080 6881/udp
VOLUME ["/data"]

HEALTHCHECK --interval=30s --timeout=5s --start-period=30s --retries=3 CMD \
  node -e "Promise.all([fetch('http://127.0.0.1:3000').then((r)=>{if(!r.ok)throw new Error('web '+r.status)}),fetch('http://127.0.0.1:8080/api/healthz').then((r)=>{if(!r.ok)throw new Error('api '+r.status)})]).then(()=>process.exit(0)).catch((err)=>{console.error(err);process.exit(1)})"

CMD ["/usr/local/bin/start-dhtsearch"]
