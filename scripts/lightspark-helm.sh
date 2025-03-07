#!/bin/bash

: "${USE_LIGHTSPARK_HELM_REPO:=false}"
: "${OPS_DIR:=$(dirname "$0")/ops}"
LIGHTSPARK_HELM_REPO="lightspark"
DEFAULT_OPS_PREFIX="${OPS_DIR}/helm"

get_helm_prefix() {
    local default_prefix=$1

    if [ "$USE_LIGHTSPARK_HELM_REPO" = true ]; then
        echo "$LIGHTSPARK_HELM_REPO"
    elif [ -n "$default_prefix" ]; then
        echo "$default_prefix"
    else
        echo "$DEFAULT_OPS_PREFIX"
    fi
}
