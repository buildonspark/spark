#!/bin/bash
set -e

source "$(dirname "$0")/ecr-secret.sh"
source "$(dirname "$0")/lightspark-helm.sh"
source "$(dirname "$0")/port-forwarding.sh"

: "${HELM_INSTALL_TIMEOUT:=2m0s}"

kubectl patch node minikube -p '{"metadata": {"labels": {"topology.kubernetes.io/zone": "minikube"}}}'

create_ecr_secret default

kubectl create namespace cert-manager
helm install \
    --atomic \
    --timeout "$HELM_INSTALL_TIMEOUT" \
    --namespace cert-manager \
    --set installCRDs=true \
    cert-manager \
    jetstack/cert-manager

sleep 5

helm install \
    --atomic \
    --timeout "$HELM_INSTALL_TIMEOUT" \
    --namespace cert-manager \
    trust-manager \
    jetstack/trust-manager

helm install \
    --atomic \
    --timeout "$HELM_INSTALL_TIMEOUT" \
    --namespace cert-manager \
    --set clusterName=minikube \
    ca \
    "$(get_helm_prefix "$OPS_DIR/terraform/cluster")/ca"

kubectl apply -n kube-system -f - <<-EOF
apiVersion: cert-manager.io/v1
kind: Certificate
metadata:
  name: ingress
spec:
  duration: 8760h
  subject:
    organizations:
      - Lightspark
  commonName: "ingress.${PROFILE}.local"
  secretName: ingress-tls
  usages:
    - server auth
  dnsNames:
    - "*.${PROFILE}.local"
    - "*.lrc20.${PROFILE}.local"
    - "*.spark.${PROFILE}.local"
    - "*.yuvd.${PROFILE}.local"
  issuerRef:
    name: ca
    kind: ClusterIssuer
EOF

postgres_installed=false
for _ in $(seq 3); do
    if helm install \
        --atomic \
        --timeout "$HELM_INSTALL_TIMEOUT" \
        postgres \
        "$(get_helm_prefix)/postgres"; then
        postgres_installed=true
        break
    fi
done
if ! $postgres_installed; then
    echo "Unable to install postgres"
    exit 1
fi

kubectl wait --for=condition=ready pod -l app.kubernetes.io/name=postgres --timeout=120s

setup_port_forward default pod/postgres-0 15432 5432
