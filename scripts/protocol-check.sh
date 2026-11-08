#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
tag=$(cat protocol/TAG)
curl -fsSL "https://raw.githubusercontent.com/kloudkit/hubd/refs/tags/$tag/protocol/client.schema.json" | diff -u - protocol/client.schema.json
echo "protocol/client.schema.json matches kloudkit/hubd $tag"
