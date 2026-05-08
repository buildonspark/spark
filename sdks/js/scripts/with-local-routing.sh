#!/usr/bin/env bash

set -euo pipefail

has_spark_ingress_hosts() {
  if ! command -v kubectl >/dev/null 2>&1; then
    return 1
  fi

  local ingress_hosts
  ingress_hosts="$(kubectl get ingress -A -o jsonpath='{range .items[*].spec.rules[*]}{.host}{"\n"}{end}' 2>/dev/null || true)"
  printf '%s\n' "$ingress_hosts" | grep -Eq '(^|\.)spark(-web)?\.minikube\.local$|(^|\.)mempool\.minikube\.local$'
}

resolve_local_ingress_host() {
  if [[ -n "${SPARK_LOCAL_INGRESS_HOST:-}" ]]; then
    printf '%s\n' "$SPARK_LOCAL_INGRESS_HOST"
    return
  fi

  if command -v kubectl >/dev/null 2>&1; then
    local current_context
    local current_context_lower
    current_context="$(kubectl config current-context 2>/dev/null || true)"
    current_context_lower="$(printf '%s' "$current_context" | tr '[:upper:]' '[:lower:]')"
    if [[ "$current_context_lower" == *kind* || "$current_context_lower" == *kdev* ]] && has_spark_ingress_hosts; then
      printf '127.0.0.1\n'
      return
    fi
  fi

  if command -v minikube >/dev/null 2>&1; then
    local minikube_ip
    minikube_ip="$(minikube ip 2>/dev/null || true)"
    if [[ -n "$minikube_ip" ]] && has_spark_ingress_hosts; then
      printf '%s\n' "$minikube_ip"
      return
    fi
  fi
}

main() {
  local ingress_host
  ingress_host="$(resolve_local_ingress_host)"
  if [[ -n "$ingress_host" ]]; then
    SPARK_LOCAL_INGRESS_HOST="$ingress_host" exec "$@"
  fi

  # Direct run-everything.sh operators use self-signed localhost certs. Keep
  # ingress runs verified by the minikube CA, and only default the local-only
  # SDK bypass when no ingress was found.
  SPARK_DANGEROUSLY_DISABLE_TLS_VERIFICATION="${SPARK_DANGEROUSLY_DISABLE_TLS_VERIFICATION:-true}" exec "$@"
}

main "$@"
