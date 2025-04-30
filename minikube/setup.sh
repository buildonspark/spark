#!/bin/sh
#
# On OS/X, the vmnet driver will allocate a new lease in /var/db/dhcpd_leases
# each time to recreate the minikube. You can remove stale entries from that
# file if you don't want that to happen.
#
set -e

PROFILE=${1:-minikube}


if [ "$(uname)" = "Darwin" ]; then
	MINIKUBE_ARGS="--driver qemu --cpus 4 --memory 16G --disk-size 128G --network socket_vmnet --socket-vmnet-path /opt/homebrew/var/run/socket_vmnet --socket-vmnet-client-path /opt/homebrew/opt/socket_vmnet/bin/socket_vmnet_client"
fi
if ! minikube status -p ${PROFILE} ; then
	minikube start -p ${PROFILE} --extra-config=apiserver.service-node-port-range=1024-65535 $MINIKUBE_ARGS
fi

minikube profile ${PROFILE}
kubectl patch node ${PROFILE} -p '{"metadata": {"labels": {"topology.kubernetes.io/zone": "minikube"}}}'

MINIKUBE_IP="$(minikube ip)"
if [ "$(uname)" = "Linux" ]; then
	BRIDGE="$(docker network ls | grep minikube | awk '{print $1}')"
	sudo resolvectl dns "br-$BRIDGE" "$MINIKUBE_IP"
	sudo resolvectl domain "br-$BRIDGE" "~minikube.local"
elif [ "$(uname)" = "Darwin" ]; then
	sudo mkdir -p /etc/resolver
	sudo tee /etc/resolver/${PROFILE} <<-EOF
		domain ${PROFILE}.local
		nameserver $MINIKUBE_IP
		timeout 5
	EOF
	sudo killall -HUP mDNSResponder
fi

helm repo add jetstack https://charts.jetstack.io
helm repo update
helm upgrade --install --namespace cert-manager --wait --create-namespace --set crds.enabled=true cert-manager jetstack/cert-manager
sleep 10
helm uninstall --ignore-not-found --namespace cert-manager cert-manager-trust
helm upgrade --install --namespace cert-manager --wait trust-manager jetstack/trust-manager
kubectl apply -f "$(dirname "$0")/ca.yaml"

echo "kube-system/ingress-tls" | minikube addons configure ingress || true
minikube addons enable ingress
minikube addons enable ingress-dns --registries IngressDNS=public.ecr.aws --images IngressDNS=h7i3i2k3/minikube-ingress-dns:0.0.3

echo "Waiting for ingress to be ready..."
while [ ! "$(kubectl -n ingress-nginx get deploy/ingress-nginx-controller -o jsonpath='{.status.readyReplicas}')" = "1" ]; do
    echo "Ingress not ready yet, checking again in 5 seconds..."
    sleep 5
done

sleep 5
kubectl get configmap cluster-ca --template='{{index .data "ca.crt"}}' > /tmp/minikube-ca.pem
if [ "$(uname)" = "Darwin" ]; then
	sudo security add-trusted-cert -d -r trustRoot -k "/Library/Keychains/System.keychain" /tmp/minikube-ca.pem
fi
