#!/usr/bin/env bash
# Build a columbo image and import it into k3s containerd on each node.
# No registry: build on an amd64 docker host (docker save), then pipe the tar
# to each k3s node's `ctr images import`. Jobs use imagePullPolicy:Never.
#
# Configure for your cluster via env (no defaults — these are site-specific):
#   COLUMBO_BUILDER   ssh target of an amd64 docker host, e.g. user@10.0.0.20
#   COLUMBO_NODES     space-separated ssh targets of the k3s nodes,
#                     e.g. "user@10.0.0.21 user@10.0.0.22"
#   IMAGE             image tag (default columbo:slim)
#   DOCKERFILE        Dockerfile path (default deploy/Dockerfile)
#
# Assumes: ssh key auth to all hosts; docker on the builder; passwordless
# `sudo k3s ctr` on the nodes.
set -euo pipefail
cd "$(dirname "$0")/.."

# Operator-local, gitignored config (hosts/IPs/ssh targets). Sourced if present
# so the site-specific values live off GitHub. See deploy/local.env.example.
[ -f deploy/local.env ] && source deploy/local.env

IMAGE="${IMAGE:-columbo:slim}"
DOCKERFILE="${DOCKERFILE:-deploy/Dockerfile}"
BUILDER="${COLUMBO_BUILDER:?set COLUMBO_BUILDER to an amd64 docker host, e.g. user@host}"
read -ra NODES <<<"${COLUMBO_NODES:?set COLUMBO_NODES to space-separated k3s node ssh targets}"
REMOTE=/tmp/columbo-build

echo "== host build static amd64 binaries =="
mkdir -p dist
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o dist/columbo ./cmd/columbo
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o dist/columbo-mcp ./cmd/columbo-mcp

echo "== stage build context on $BUILDER =="
ssh "$BUILDER" "rm -rf $REMOTE && mkdir -p $REMOTE/dist"
scp -q dist/columbo dist/columbo-mcp "$BUILDER:$REMOTE/dist/"
scp -q "$DOCKERFILE" "$BUILDER:$REMOTE/Dockerfile"
tar -cf - examples | ssh "$BUILDER" "tar -xf - -C $REMOTE"

echo "== docker build $IMAGE (from $DOCKERFILE) on $BUILDER =="
ssh "$BUILDER" "cd $REMOTE && docker build -t $IMAGE -f Dockerfile . && docker save $IMAGE -o /tmp/columbo-img.tar && ls -lh /tmp/columbo-img.tar"

echo "== import to k3s nodes =="
for n in "${NODES[@]}"; do
  echo "-- import $IMAGE on $n"
  ssh "$BUILDER" "cat /tmp/columbo-img.tar" | ssh "$n" "sudo k3s ctr images import -"
done
echo "== done: $IMAGE on ${NODES[*]} =="
