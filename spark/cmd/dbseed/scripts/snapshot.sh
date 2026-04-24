#!/usr/bin/env bash
# snapshot.sh — pg_dump a seeded SO database to a stamped file.
#
# Usage:
#   snapshot.sh [--minikube] [--out DIR] [--compress LEVEL] DBNAME
#
#   --minikube          Run pg_dump inside the minikube postgres-0 pod via
#                       kubectl exec, then stream the file out via stdin.
#   --out DIR           Where to save the dump. Default: <dbseed-dir>/snapshots
#                       (gitignored; see spark/spark/.gitignore).
#   --compress LEVEL    pg_dump -Z compression level. Default: 1 (fastest).
#                       0 = no compression, 9 = smallest. On the full-profile
#                       dataset (~61M transfers), -Z1 finishes in ~5 min and
#                       produces ~15 GB; -Z6 is ~4× slower (~20+ min) for
#                       ~20% smaller output — not worth it for local dev.
#                       Override to -Z0 for maximum speed if disk is cheap.
#   DBNAME              SO database name (e.g., sparkoperator_0).
#
# Produces a custom-format dump (pg_restore -Fc input) at DIR/<dbname>-<timestamp>.pgdump.
# Custom format is ~5-10× smaller than plain SQL and restores with parallelism.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
USE_MINIKUBE=0
OUT_DIR="${SCRIPT_DIR}/../snapshots"
COMPRESS=1

while [[ $# -gt 0 ]]; do
  case "$1" in
    --minikube) USE_MINIKUBE=1; shift ;;
    --out) OUT_DIR="$2"; shift 2 ;;
    --compress) COMPRESS="$2"; shift 2 ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    -*) echo "unknown flag: $1" >&2; exit 2 ;;
    *) DBNAME="$1"; shift ;;
  esac
done

if [[ -z "${DBNAME:-}" ]]; then
  echo "error: DBNAME is required" >&2
  exit 2
fi

mkdir -p "${OUT_DIR}"
TS="$(date -u +%Y%m%dT%H%M%SZ)"
OUT_FILE="${OUT_DIR}/${DBNAME}-${TS}.pgdump"

echo "dumping ${DBNAME} with -Z${COMPRESS} (minikube=${USE_MINIKUBE}) to ${OUT_FILE}"

if [[ "${USE_MINIKUBE}" == "1" ]]; then
  # -Fc = custom (compressed, selective-restore-capable).
  # --no-owner / --no-privileges keeps the dump portable across roles.
  kubectl exec -n default postgres-0 -- \
    pg_dump -U postgres -d "${DBNAME}" -Fc -Z"${COMPRESS}" --no-owner --no-privileges \
    > "${OUT_FILE}"
else
  pg_dump -d "${DBNAME}" -Fc -Z"${COMPRESS}" --no-owner --no-privileges -f "${OUT_FILE}"
fi

SIZE="$(du -h "${OUT_FILE}" | cut -f1)"
echo "snapshot written: ${OUT_FILE} (${SIZE})"
