# syntax=docker/dockerfile:1

# ---- build ------------------------------------------------------------------
FROM golang:1.24-alpine AS build

# What the image calls itself. Pass it through at build time —
# `docker build --build-arg VERSION=v1.2.3 .` — so the running server reports
# the release it came from rather than a bare "dev".
ARG VERSION=dev

WORKDIR /src

# The module has no third-party dependencies, so this layer needs no network
# and stays cached until go.mod itself changes.
COPY go.mod ./
RUN go mod download

COPY . .

# CGO off gives a static binary with no loader or libc to carry. -trimpath keeps
# build paths out of it; -s -w drop the symbol and DWARF tables.
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags="-s -w -X github.com/claesp/wacky/internal/version.version=${VERSION}" \
      -o /out/wacky ./cmd/wacky

# ---- git ---------------------------------------------------------------------
# wacky reads the repository by running git, so the runtime image needs a git
# binary. Rather than carry a whole distribution for it, this stage collects git
# and only the shared objects it actually links against — three of them, about
# 4 MB in total, resolved from ldd so the list cannot drift or miss an
# architecture.
FROM alpine:3.21 AS gitfs

RUN apk add --no-cache git

RUN mkdir -p /rootfs/usr/bin /rootfs/etc \
 && cp /usr/bin/git /rootfs/usr/bin/git \
 && ldd /usr/bin/git | tr -s '[:blank:]' '\n' | grep '^/' | sort -u \
    | xargs -I{} sh -c 'mkdir -p /rootfs$(dirname {}) && cp -L {} /rootfs{}' \
 && printf 'wacky:x:10001:10001::/nonexistent:/sbin/nologin\n' > /rootfs/etc/passwd \
 && printf 'wacky:x:10001:\n' > /rootfs/etc/group \
 && install -d -m 1777 /rootfs/tmp

# ---- runtime -----------------------------------------------------------------
FROM scratch

COPY --from=gitfs /rootfs/ /
COPY --from=build /out/wacky /usr/local/bin/wacky

# The default address binds the loopback interface, which nothing outside the
# container can reach; in a container it has to listen on all of them.
ENV WACKY_ADDR=0.0.0.0:8080 \
    WACKY_GIT_REPO=/repo

# A repository mounted from the host belongs to a different uid than the one
# this container runs as, and git refuses to touch it without this. wacky only
# ever reads — it runs no hooks and writes nothing — so trusting the path the
# operator deliberately mounted is safe here.
ENV GIT_CONFIG_COUNT=1 \
    GIT_CONFIG_KEY_0=safe.directory \
    GIT_CONFIG_VALUE_0=*

USER wacky
EXPOSE 8080

# The image has no shell or HTTP client, so the binary probes itself. Exec form
# is required: there is no shell to parse a string. Under Kubernetes prefer an
# httpGet probe, which the kubelet performs without entering the container.
HEALTHCHECK --interval=30s --timeout=5s --start-period=5s \
  CMD ["/usr/local/bin/wacky", "-health-check"]

# Flags given to `run` are appended, so `run … -git-ref v1.0` works.
ENTRYPOINT ["/usr/local/bin/wacky"]
