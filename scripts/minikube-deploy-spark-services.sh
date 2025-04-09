#!/bin/bash

set -e

source "$(dirname "$0")/ecr-secret.sh"
source "$(dirname "$0")/port-forwarding.sh"
source "$(dirname "$0")/lightspark-helm.sh"

# Forces the DBs to be recreated
: "${RESET_DBS:=true}"
# Allow using dev spark image
: "${USE_DEV_SPARK:=false}"
: "${HELM_INSTALL_TIMEOUT:=2m0s}"
: "${SPARK_TAG:=latest}"
: "${LRC20_TAG:=latest}"
# shellcheck disable=SC2119
HELM_REPO_PREFIX=$(get_helm_prefix)

PRIV_KEYS=(
    "5eaae81bcf1fd43fbb92432b82dbafc8273bb3287b42cb4cf3c851fcee2212a5"
    "bc0f5b9055c4a88b881d4bb48d95b409cd910fb27c088380f8ecda2150ee8faf"
    "d5043294f686bc1e3337ce4a44801b011adc67524175f27d7adc85d81d6a4545"
    "f2136e83e8dc4090291faaaf5ea21a27581906d8b108ac0eefdaecf4ee86ac99"
    "effe79dc2a911a5a359910cb7782f5cabb3b7cf01e3809f8d323898ffd78e408"
)

PUB_KEYS=(
    "0322ca18fc489ae25418a0e768273c2c61cabb823edfb14feb891e9bec62016510"
    "0341727a6c41b168f07eb50865ab8c397a53c7eef628ac1020956b705e43b6cb27"
    "0305ab8d485cc752394de4981f8a5ae004f2becfea6f432c9a59d5022d8764f0a6"
    "0352aef4d49439dedd798ac4aef1e7ebef95f569545b647a25338398c1247ffdea"
    "02c05c88cc8fc181b1ba30006df6a4b0597de6490e24514fbdd0266d2b9cd3d0ba"
)

# Use minikube's docker environment for local images
if [ "$USE_DEV_SPARK" = "true" ]; then
    echo "Using minikube docker environment for dev builds..."
    eval "$(minikube docker-env)"

    # Verify dev images exist
    if ! docker image inspect spark:dev >/dev/null 2>&1; then
        echo "Error: spark:dev image not found. Please run build.sh first."
        exit 1
    fi
    echo "Using local spark:dev image"
    SPARK_REPO="spark"
    SPARK_TAG="dev"
else
    echo "Using remote spark image: ${SPARK_REPO:-ecr}:${SPARK_TAG:-latest}"
fi

for NAMESPACE in spark lrc20 bitcoin; do
    kubectl get namespace "$NAMESPACE" >/dev/null 2>&1 || kubectl create namespace "$NAMESPACE"
    create_ecr_secret "$NAMESPACE"
done

helm upgrade \
    --install \
    --timeout "$HELM_INSTALL_TIMEOUT" \
    --namespace bitcoin \
    --set imagePullSecret=ecr \
    --set resources.requests.memory=1Gi \
    --set config.network=regtest \
    --set testutil.enabled=true \
    --set priority_class.enabled=false \
    regtest \
    "$HELM_REPO_PREFIX"/bitcoind &

helm upgrade \
    --install \
    --timeout "$HELM_INSTALL_TIMEOUT" \
    --namespace bitcoin \
    --set imagePullSecret=ecr \
    --set network="regtest" \
    --set yuvd.namespace="lrc20" \
    --set ingress.domain=mempool.minikube.local \
    regtest-mempool \
    "$HELM_REPO_PREFIX"/mempool &

helm upgrade \
    --install \
    --timeout "$HELM_INSTALL_TIMEOUT" \
    --namespace bitcoin \
    --set imagePullSecret=ecr \
    --set network="regtest" \
    regtest-electrs \
    "$HELM_REPO_PREFIX"/electrs &

last_so_index=$((${#PRIV_KEYS[@]} - 1))

if [ "$RESET_DBS" = "true" ]; then
    echo "Cleaning up databases..."
    for i in $(seq 0 $last_so_index); do
        echo "Dropping database sparkoperator_${i}..."
        kubectl exec -n default postgres-0 -- psql -U postgres -c "DROP DATABASE IF EXISTS \"sparkoperator_${i}\";" || true
        echo "Dropping database lrc20_${i}..."
        kubectl exec -n default postgres-0 -- psql -U postgres -c "DROP DATABASE IF EXISTS \"lrc20_${i}\";" || true
    done
fi

for i in $(seq 0 $last_so_index); do
    echo "Creating database sparkoperator_${i}..."
    kubectl exec -n default postgres-0 -- psql -U postgres -c "CREATE DATABASE \"sparkoperator_${i}\";" || true
    echo "Creating database lrc20_${i}..."
    kubectl exec -n default postgres-0 -- psql -U postgres -c "CREATE DATABASE \"lrc20_${i}\";" || true
done

kubectl create secret generic -n spark regtest-spark \
    --from-literal="operator0.key=${PRIV_KEYS[0]}" \
    --from-literal="operator1.key=${PRIV_KEYS[1]}" \
    --from-literal="operator2.key=${PRIV_KEYS[2]}" \
    --from-literal="operator3.key=${PRIV_KEYS[3]}" \
    --from-literal="operator4.key=${PRIV_KEYS[4]}" \
    --dry-run=client -o yaml | kubectl apply -f -

pubkeys_json=$(printf '%s\n' "${PUB_KEYS[@]}" | jq -R . | jq -s .)
operator_cmd=(
    helm install
    --version 0.1.1
    --timeout "$HELM_INSTALL_TIMEOUT"
    --namespace spark
    --set config.network="regtest"
    --set config.db_uri="postgresql://postgres@postgres.default:5432/sparkoperator_\${INDEX}?sslmode=disable"
    --set config.aws=false
    --set config.integration_test=true
    --set config.threshold=3
    --set config.withdrawbondsats=1000000
    --set ingress.enabled=true
    --set ingress.domain=spark.minikube.local
    --set imagePullSecret="ecr"
    --set-json "pubkeys=$pubkeys_json"
    --set yuvd.namespace="lrc20"
    --set bitcoind.namespace="bitcoin"
    --set operator.image.tag="$SPARK_TAG"
    --set signer.image.tag="$SPARK_TAG"
)

if [ -n "$SPARK_REPO" ]; then
    operator_cmd+=(
        --set operator.image.repository="$SPARK_REPO"
        --set operator.image.pullPolicy=Never
        --set signer.image.repository="$SPARK_REPO"
        --set signer.image.pullPolicy=Never
    )
fi

if [ -n "$SPARK_TAG" ]; then
    operator_cmd+=(--set "operator.image.tag=$SPARK_TAG")
fi

operator_cmd+=(
    regtest
    "$HELM_REPO_PREFIX"/spark
)

"${operator_cmd[@]}" &

# Deploy LRC20 nodes
lrc20_replicas=3
helm install \
    --timeout "$HELM_INSTALL_TIMEOUT" \
    --namespace lrc20 \
    --set replicas=$lrc20_replicas \
    --set config.network="regtest" \
    --set image.tag="$LRC20_TAG" \
    --set config.database_url="postgresql://postgres@postgres.default:5432/lrc20_\${INDEX}" \
    --set config.extra.bnode.url="http://regtest-bitcoind.bitcoin:8332" \
    --set ingress.enabled=true \
    --set ingress.domain=lrc20.minikube.local \
    --set storage.class="standard" \
    --set imagePullSecret="ecr" \
    --set config.extra.indexer.confirmations_number=1 \
    regtest \
    "$HELM_REPO_PREFIX"/yuvd &

check_service_readiness() {
    local namespace=$1
    local service_name=$2
    local label_selector="app.kubernetes.io/name in (${service_name})"

    local pods_json pods_count non_ready_pods non_ready_count
    pods_json=$(kubectl get pods -n "$namespace" -l "$label_selector" -o json |
        jq '{pods: [.items[] | {
            name: .metadata.name,
            phase: .status.phase,
            containerStatuses: .status.containerStatuses
        }]}')

    pods_count=$(echo "$pods_json" | jq '.pods | length')
    if [ "$pods_count" -eq 0 ]; then
        echo "No $service_name.${namespace} pods found yet"
        return 1
    fi

    non_ready_pods=$(echo "$pods_json" | jq '
    {
        non_compliant_pods: [
        .pods[] |
        {
            name: .name,
            issues: (
            [] +
            # Check if phase is not Running
            (if (.phase | ascii_downcase) != "running"
                then ["Pod phase is \"\(.phase)\" instead of \"Running\""]
                else []
            end) +
            # Check if any containers are not ready - handle null containerStatuses
            (if .containerStatuses == null then
                ["Container statuses not available yet"]
             else
                [.containerStatuses[] | select(.ready != true) | "Container \"\(.name)\" is not ready"]
             end)
            )
        } |
        select(.issues | length > 0)  # Keep only pods with issues
        ]
    }')

    non_ready_count=$(echo "$non_ready_pods" | jq '.non_compliant_pods | length')
    if [ "$non_ready_count" -gt 0 ]; then
        echo "$service_name.${namespace} pods not ready ($non_ready_count total):"
        echo "$non_ready_pods" | jq -r '.non_compliant_pods[] | "  - \(.name): \(.issues | join(", "))"'
        return 1
    else
        echo "All $service_name.${namespace} pods ready"
        return 0
    fi
}

sleep 10

echo "Waiting for all services to be ready..."
max_attempts=12
for attempt in $(seq 1 $max_attempts); do
    echo "Check attempt $attempt/$max_attempts..."
    all_ready=true

    check_service_readiness "spark" "spark" || all_ready=false
    check_service_readiness "bitcoin" "bitcoind" || all_ready=false
    check_service_readiness "bitcoin" "electrs" || all_ready=false
    check_service_readiness "bitcoin" "mempool" || all_ready=false
    check_service_readiness "lrc20" "yuvd" || all_ready=false

    if $all_ready; then
        echo "All services are ready!"
        break
    fi

    if [[ $attempt -eq $max_attempts && "$all_ready" == "false" ]]; then
        echo "ERROR: Not all pods are ready after $max_attempts attempts!"
        exit 1
    fi

    echo "Waiting 10 seconds before next check..."
    sleep 10
done

setup_port_forward bitcoin service/regtest-bitcoind 8332 8332
setup_port_forward bitcoin service/regtest-bitcoind 8330 8330
setup_port_forward bitcoin service/regtest-bitcoind 8331 8331

for i in $(seq 0 $last_so_index); do
    setup_port_forward spark service/regtest-spark-"${i}" $((8535 + i)) 8000
done

for i in $(seq 0 $((lrc20_replicas - 1))); do
    setup_port_forward lrc20 pod/regtest-yuvd-"${i}" $((18330 + i)) 8000
    setup_port_forward lrc20 pod/regtest-yuvd-"${i}" $((18530 + i)) 8002
done

sleep 2
