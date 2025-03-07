#!/bin/bash

create_ecr_secret() {
    local namespace=$1

    if [ -f "/home/runner/.docker/config.json" ]; then
        # GitHub Actions environment
        kubectl -n "$namespace" create secret docker-registry ecr \
            --from-file=.dockerconfigjson=/home/runner/.docker/config.json
    else
        PASSWORD="$(aws ecr get-login-password)"
        SERVER="674966927423.dkr.ecr.us-west-2.amazonaws.com"
        kubectl -n "$namespace" delete --ignore-not-found secret ecr
        kubectl -n "$namespace" create secret docker-registry ecr --docker-server="$SERVER" --docker-username=AWS --docker-password="$PASSWORD"
    fi
}
