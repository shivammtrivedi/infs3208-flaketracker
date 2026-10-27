# One Dockerfile builds EVERY Go service. Which one is chosen with a build argument:
#   docker build -f build/go.Dockerfile --build-arg SERVICE=ingest -t flaketracker/ingest .
# (SERVICE is a folder name under cmd/: ingest, api, analyzer, migrate or seed.)
#
# It is a "multi-stage" build: stage 1 has the whole Go toolchain and compiles
# the program; stage 2 is a tiny image containing ONLY the compiled binary.
# The toolchain never ships, so the final image is ~20 MB instead of ~800 MB
# (small images pull fast onto GKE nodes) and has almost nothing to attack.

# ---------- stage 1: compile ----------
FROM golang:1.25-alpine AS build

# Which cmd/<service> to build.
ARG SERVICE
# Build metadata stamped into the binary (shown in the dashboard footer).
# VERSION and COMMIT identify the build; ALGORITHM picks the analyzer's
# scoring algorithm (v1 or v2) - baked in at build time, so the v2 rollout is
# a genuinely different image rather than a config flip.
ARG VERSION=dev
ARG COMMIT=unknown
ARG ALGORITHM=v1

WORKDIR /src

# Copy only the dependency lists first and download. Docker caches each step,
# so this slow step is skipped on later builds unless go.mod/go.sum change.
COPY go.mod go.sum ./
RUN go mod download

COPY cmd ./cmd
COPY internal ./internal

# CGO_ENABLED=0   produce a fully static binary (needed to run on distroless, which has no C library)
# -trimpath       remove local file paths from the binary
# -s -w           strip debug symbols to make the binary smaller
# -X pkg.Var=val  overwrite a string variable in the Go code at link time
RUN CGO_ENABLED=0 go build -trimpath \
      -ldflags "-s -w \
        -X flaketracker/internal/version.Version=${VERSION} \
        -X flaketracker/internal/version.Commit=${COMMIT} \
        -X flaketracker/internal/version.Algorithm=${ALGORITHM}" \
      -o /out/app ./cmd/${SERVICE}

# ---------- stage 2: the runtime image ----------
# distroless/static: no shell, no package manager, no curl - just CA certificates
# and time-zone data. The ":nonroot" variant runs as an unprivileged user (uid 65532).
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/app /app

# The SQL migrations. Only the `migrate` service reads them; they are a few KB,
# so every image carries them rather than needing a separate Dockerfile.
COPY db/migrations /db/migrations

# The migrate tool looks for "db/migrations" relative to its working directory.
# distroless starts in /home/nonroot, which would not find it, so start in "/".
WORKDIR /

USER nonroot
ENTRYPOINT ["/app"]
