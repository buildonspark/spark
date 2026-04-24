#!/usr/bin/env bash
# restore.sh — pg_restore a dbseed snapshot into an SO database.
#
# Default behavior passes --clean --if-exists to pg_restore, which drops and
# recreates every object present in the dump before reloading it. This makes
# restore work correctly against a dirty DB (partial deletes, stale data) —
# tables outside the dump are left alone. Use --drop for the full sledgehammer.
#
# Usage:
#   restore.sh [--minikube] [--drop] [--jobs N] DBNAME SNAPSHOT
#
#   --minikube    Pipe the snapshot into pg_restore running inside the
#                 minikube postgres-0 pod via kubectl exec.
#   --drop        Drop and recreate the target database first. Destroys any
#                 existing data including tables not in the dump; no undo.
#                 Use when the DB is corrupted or you want a pristine restore.
#   --jobs N      Parallel restore workers. Default: 4. Ignored for --minikube
#                 because the stream variant doesn't support -j.
#   DBNAME        Target SO database name.
#   SNAPSHOT      Path to a .pgdump file from snapshot.sh.

set -euo pipefail

USE_MINIKUBE=0
DROP=0
JOBS=4

while [[ $# -gt 0 ]]; do
  case "$1" in
    --minikube) USE_MINIKUBE=1; shift ;;
    --drop) DROP=1; shift ;;
    --jobs) JOBS="$2"; shift 2 ;;
    -h|--help) sed -n '2,17p' "$0"; exit 0 ;;
    -*) echo "unknown flag: $1" >&2; exit 2 ;;
    *)
      if [[ -z "${DBNAME:-}" ]]; then DBNAME="$1"
      else SNAPSHOT="$1"
      fi
      shift
      ;;
  esac
done

if [[ -z "${DBNAME:-}" || -z "${SNAPSHOT:-}" ]]; then
  echo "error: DBNAME and SNAPSHOT are required" >&2
  exit 2
fi
if [[ ! -f "${SNAPSHOT}" ]]; then
  echo "error: snapshot file not found: ${SNAPSHOT}" >&2
  exit 2
fi

echo "restoring ${SNAPSHOT} -> ${DBNAME} (minikube=${USE_MINIKUBE} drop=${DROP} jobs=${JOBS})"

# --clean --if-exists is off when --drop was used, since the DB was just
# recreated empty and there's nothing to clean.
CLEAN_FLAGS=(--clean --if-exists)
if [[ "${DROP}" == "1" ]]; then
  CLEAN_FLAGS=()
fi

if [[ "${USE_MINIKUBE}" == "1" ]]; then
  if [[ "${DROP}" == "1" ]]; then
    kubectl exec -n default postgres-0 -- psql -U postgres -d postgres \
      -c "DROP DATABASE IF EXISTS \"${DBNAME}\";" \
      -c "CREATE DATABASE \"${DBNAME}\";"
  fi
  # Stream the .pgdump into pg_restore via stdin. Parallelism requires a
  # seekable file, which stdin isn't — so --jobs is only used in the local
  # branch. For most local workflows this single-threaded restore is still
  # fast (a few minutes for the full profile's dumps).
  kubectl exec -i -n default postgres-0 -- \
    pg_restore -U postgres -d "${DBNAME}" --no-owner --no-privileges "${CLEAN_FLAGS[@]}" \
    < "${SNAPSHOT}"
else
  if [[ "${DROP}" == "1" ]]; then
    psql -d postgres \
      -c "DROP DATABASE IF EXISTS \"${DBNAME}\";" \
      -c "CREATE DATABASE \"${DBNAME}\";"
  fi
  pg_restore -d "${DBNAME}" -j "${JOBS}" --no-owner --no-privileges "${CLEAN_FLAGS[@]}" "${SNAPSHOT}"
fi

echo "restore complete"
