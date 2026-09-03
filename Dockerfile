# Build context contract: this Dockerfile consumes an ALREADY-BUILT `helixon`
# binary sitting at the root of the build context. It does not compile
# anything, and `docker build .` from a source checkout will not work -- there
# is no `helixon` binary at the repo root. Stage a context first; see
# .github/workflows/image.yml, which does exactly that.
#
# It is written this way because its only release consumer is the `dockers:`
# section of .goreleaser.yaml, and goreleaser's docker build context is a temp
# directory holding only the built binaries, the packages, this dockerfile and
# any `extra_files` -- the source tree is NOT there. A Dockerfile that compiles
# from source therefore cannot work under `dockers:` at all: it dies on
# `COPY go.mod go.sum ./` with `copier: stat: "/go.mod": no such file or
# directory` long before it reaches the build step. That is not a tunable; it
# is the shape of the context.
#
# Consuming the goreleaser binary also fixes two things the builder stage got
# wrong beyond failing outright:
#
#   * The image now ships the SAME artifact as the release tarball. The
#     `builds:` block sets CGO_ENABLED=0; the old builder stage set
#     CGO_ENABLED=1, so the container and the tarball published under one
#     version tag were two different binaries.
#   * The arm64 image no longer recompiles under emulation. goreleaser
#     cross-compiles the arm64 binary on the host, and FROM/COPY/ENTRYPOINT
#     execute no target-architecture instructions, so the arm64 image builds
#     with no QEMU binfmt handler present at all.
#
# Base is `static`, not `base`: nothing in this module imports "C", and
# modernc.org/sqlite is pure Go, so the CGO_ENABLED=0 binary is statically
# linked ("not a dynamic executable") and needs no glibc. `static` still
# carries ca-certificates, /etc/passwd and tzdata, which the runtime does use.
FROM gcr.io/distroless/static-debian12:nonroot

COPY helixon /helixon

ENTRYPOINT ["/helixon"]
