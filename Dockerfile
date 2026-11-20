# Stage 1: Build Go server
FROM golang:1.24-alpine AS builder

WORKDIR /src
COPY go.mod ./
COPY submodules/ip2region/binding/golang ./submodules/ip2region/binding/golang
COPY cmd ./cmd

RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /bin/ip2region-server ./cmd/server

# Stage 2: Runtime image
FROM python:3.12-alpine

RUN apk add --no-cache bash curl tzdata ca-certificates

WORKDIR /app

COPY --from=builder /bin/ip2region-server /usr/local/bin/ip2region-server
COPY updater.py /app/updater.py
COPY scripts/cleanup.sh /app/scripts/cleanup.sh
COPY scripts/entrypoint.sh /app/scripts/entrypoint.sh
COPY submodules/ip2region/data/ip2region_v4.xdb /app/seed/ip2region_v4.xdb
COPY submodules/ip2region/data/ip2region_v6.xdb /app/seed/ip2region_v6.xdb

RUN chmod +x /usr/local/bin/ip2region-server /app/scripts/cleanup.sh /app/scripts/entrypoint.sh

ENV IP2REGION_DATA_DIR=/var/lib/ip2region \
    PORT=28080 \
    PATH_PREFIX=/ifconfig \
    CACHE_POLICY=vindex \
    SEARCHERS=50 \
    UPDATE_INTERVAL=6h \
    PYTHONUNBUFFERED=1

VOLUME ["/var/lib/ip2region"]
EXPOSE 28080

ENTRYPOINT ["/bin/bash", "/app/scripts/entrypoint.sh"]
