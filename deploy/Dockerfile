# columbo:slim — slice-1 image. Prebuilt static linux/amd64 binaries (built on
# the host via `deploy/build-and-import.sh`, so the heavy golang build stage is
# not emulated on an arm64 Mac) plus the examples. Alpine gives a PATH, /tmp,
# and CA certs without the pitfalls of scratch; the binaries are CGO_ENABLED=0
# static, so they run regardless of musl.
FROM --platform=linux/amd64 alpine:3.20

COPY dist/columbo /usr/local/bin/columbo
COPY dist/columbo-mcp /usr/local/bin/columbo-mcp
COPY examples /examples

ENTRYPOINT ["columbo"]
