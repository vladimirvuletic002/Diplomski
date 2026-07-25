# Multi-stage build: compile with the full Go toolchain, ship only the binary.
#
# Both base images are pinned by digest rather than tag. A thesis needs the build
# to be reproducible months later, and a tag like `1.26.5` can be re-pushed.

# --- build stage ------------------------------------------------------------
FROM golang@sha256:3aff6657219a4d9c14e27fb1d8976c49c29fddb70ba835014f477e1c70636647 AS build

WORKDIR /src

# Dependencies are copied and downloaded before the source, so editing a .go file
# does not invalidate the cached module download layer.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# The version is a build argument, never a runtime environment variable: baking it
# in ties the version a container reports to the image tag it shipped in. If the
# two could drift, a canary's metrics would be attributed to the wrong version and
# the rollout analysis would draw its conclusions from mislabelled data.
ARG VERSION=dev

# CGO is disabled to produce a fully static binary — nothing in the dependency
# tree needs it, and it is what allows the distroless static base below.
RUN CGO_ENABLED=0 GOOS=linux go build \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/service .

# --- runtime stage ----------------------------------------------------------
# distroless/static holds no shell and no package manager, which keeps the attack
# surface minimal. Unlike scratch it still provides CA certificates and a nonroot
# user, so outbound TLS and a non-root UID work without hand-assembling them.
FROM gcr.io/distroless/static-debian12@sha256:aef9602f8710ec12bde19d593fed1f76c708531bb7aba205110f1029786ead7b

COPY --from=build /out/service /service

# Documentation only; the port is configurable via PORT.
EXPOSE 8081

# Exec form, so the binary is PID 1 and receives SIGTERM directly. Without this a
# shell would swallow the signal and Kubernetes would SIGKILL the container after
# the grace period, cutting in-flight requests — which the rollout analysis would
# read as a failing release.
ENTRYPOINT ["/service"]
