#!/bin/bash
set -e

PGDATABASE="spark-0"
PGUSER="sparkro"

usage() {
	echo "Usage: $0 <options> <dev|loadtest|prod>"
	echo "Options:"
	echo "  -a             Use admin access"
	echo "  -d DATABASE    Connect to specified database"
	echo "  -w             Enable write access"
	exit 1
}

while getopts "ad:ew" OPTION; do
	case $OPTION in
	a)
		PGUSER="postgres"
		;;
	d)
		PGDATABASE="$OPTARG";;
	w)
		PGUSER="sparkrw"
		;;
	*)
		usage;;
	esac
done
shift $((OPTIND-1))
if [ $# != 1 ]; then usage; fi

CONTEXT="${1:?Specify Kubernetes context name}"
if ! [[ "$CONTEXT" =~ ^(brett|dev|loadtest|mgorven|prod)$ ]]; then
    echo "Invalid Kubernetes context: $CONTEXT"
    usage
fi

AUTHORITY="$(kubectl --context "$CONTEXT" -n spark get statefulset spark -o yaml -o jsonpath='{.spec.template.spec.containers[0].command[-1]}' | grep "database=" | cut -d/ -f3)"
PORT="${AUTHORITY#*:}"
HOSTPORT="${AUTHORITY#*@}"
HOST="${HOSTPORT%:*}"

PGPASSWORD="$(aws rds generate-db-auth-token --hostname "$HOST" --port "$PORT" --username "$PGUSER")"
POD="postgres-$USER-$(date +%s)"
OVERRIDES='{"spec": {"nodeSelector": {"lightspark.com/nodegroup": "private"}}}'

echo "Starting pod $POD"
kubectl --context "$CONTEXT" run -it "$POD" --restart=Never --image=postgres:17 --env "PGPASSWORD=$PGPASSWORD" --overrides "$OVERRIDES" -- psql -U $PGUSER -h $HOST -p $PORT $PGDATABASE
