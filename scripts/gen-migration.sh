#!/bin/bash
set -e

CURRENT_DIR=$(dirname "$0")
SPARK_SO_DIR=$(realpath "$CURRENT_DIR/../spark")

DATE=$(date +%Y%m%d%H%M%S)
DB_NAME=sparkoperator_migration_temp_$DATE

if [ -z "$1" ]; then
    echo "Usage: $0 <diff_name>"
    exit 1
fi

DIFF_NAME=$1

if [[ ! "$DIFF_NAME" =~ ^[a-z_]+$ ]]; then
    echo "Invalid dif name: only lowercase letters and underscores are allowed."
    exit 1
fi

echo "Creating temporary database $DB_NAME"

createdb $DB_NAME

cd $SPARK_SO_DIR
atlas migrate diff $DIFF_NAME \
    --dir "file://so/ent/migrate/migrations" \
    --to "ent://so/ent/schema" \
    --dev-url "postgresql://127.0.0.1:5432/$DB_NAME?sslmode=disable&search_path=public"

dropdb $DB_NAME || echo "Unable to drop temporary database $DB_NAME, you can drop it manually with \"dropdb $DB_NAME\"."

echo "Migration $DIFF_NAME has been generated."
