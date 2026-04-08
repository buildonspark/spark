#!/bin/bash
set -e

# Create operator databases and run atlas migrations.
# This runs inside the postgres container via docker-entrypoint-initdb.d.

for i in 0 1 2; do
  echo "Creating databases for operator $i..."
  psql -v ON_ERROR_STOP=1 --username "$POSTGRES_USER" <<-EOSQL
    CREATE DATABASE sparkoperator_$i;
    CREATE DATABASE spark_ephemeral_$i;
EOSQL
done

echo "All databases created."
