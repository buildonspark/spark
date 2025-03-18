#!/bin/bash

set -e

GIT_HASH_FULL="$(git log -1 --format='%H' spark signer)"
GIT_HASH="${GIT_HASH_FULL:0:8}"
DATE="$(date -u '+%Y%m%d')"

echo "🐳 Building new Spark image (git_${GIT_HASH})..."

docker buildx build \
    -f Dockerfile --platform linux/arm64 \
    --label "org.opencontainers.image.author=${USER}" \
    --label "org.opencontainers.image.created=$(date -u +"%Y-%m-%dT%H:%M:%SZ")" \
    --label "org.opencontainers.image.description=A new Bitcoin layer 2 protocol" \
    --label "org.opencontainers.image.licenses=" \
    --label "org.opencontainers.image.revision=${GIT_HASH_FULL}" \
    --label "org.opencontainers.image.source=https://github.com/lightsparkdev/spark" \
    --label "org.opencontainers.image.title=spark" \
    --label "org.opencontainers.image.url=https://github.com/lightsparkdev/spark" \
    --label "org.opencontainers.image.vendor=Lightspark" \
    --label "org.opencontainers.image.version=main" \
    --tag 674966927423.dkr.ecr.us-west-2.amazonaws.com/spark-go:git_${GIT_HASH} \
    --tag 674966927423.dkr.ecr.us-west-2.amazonaws.com/spark-go:git_${DATE}_${GIT_HASH} \
    --tag 674966927423.dkr.ecr.us-west-2.amazonaws.com/spark-go:latest \
    --provenance=false \
    --push \
    .

echo "🚀 Restarting Spark..."

kubectl --context dev -n spark rollout restart sts/spark
