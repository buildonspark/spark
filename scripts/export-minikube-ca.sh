#!/bin/bash

MINIKUBE_CA_FILE="/tmp/minikube-ca.pem"

kubectl get configmap cluster-ca --template='{{index .data "ca.crt"}}' > "$MINIKUBE_CA_FILE"
