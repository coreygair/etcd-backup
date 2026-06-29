FROM golang:1.26 AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=linux \
    go build \
        -trimpath \
        -ldflags="-s -w" \
        -o /out/etcd-backup .

# Create scratch image rootfs.
# - Application binary
# - Public CA certificates for TLS/HTTPS
# - /tmp
# - A non-root user & group
RUN set -eux; \
    mkdir -p /rootfs/etc/ssl/certs /rootfs/tmp; \
    cp /out/etcd-backup /rootfs/etcd-backup; \
    cp -a /etc/ssl/certs/. /rootfs/etc/ssl/certs/; \
    chmod 1777 /rootfs/tmp; \
    echo 'nonroot:x:1000:1000:nonroot:/:/sbin/nologin' > /rootfs/etc/passwd; \
    echo 'nonroot:x:1000:' > /rootfs/etc/group

FROM scratch AS etcd-backup
COPY --from=build /rootfs /
USER nonroot:nonroot
ENTRYPOINT ["/etcd-backup"]
