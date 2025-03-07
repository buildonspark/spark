# spark-go

## Generate proto files

Protobuf needs to be installed in order to build the project.

```
brew install protobuf
```

Go protobuf tools need to be installed as well:

```
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
go install github.com/envoyproxy/protoc-gen-validate@latest
```

After modifying the proto files, you can generate the Go files with the following command:

```
make
```

## Bitcoind

Our SO implementation uses ZMQ to listen for block updates from bitcoind. Install it with:

```
brew install zeromq
```

Note: whatever bitcoind you are running will also need to have been compiled with ZMQ.
The default installation via brew has ZMQ, but binaries downloaded from the bitcoin core
website do not.

```
brew install bitcoin
```

## DB Migrations

We use atlas to manage our database migrations. Install it [here](https://atlasgo.io/getting-started/#installation).

To make a migration, follow these steps:

- Make your change to the schema, run `make ent`
- Generate migration files by running (from spark directory):

```
createdb sparkoperator_temp
atlas migrate diff <diff_name> \
--dir "file://so/ent/migrate/migrations" \
--to "ent://so/ent/schema" \
--dev-url "postgresql://127.0.0.1:5432/sparkoperator_temp?sslmode=disable&search_path=public"
dropdb sparkoperator_temp
```

- When running `run-everything.sh`, the migration will be automatically
  applied to each operator's database. But if you want to apply a migration manually, you can run (e.g. DB name is `sparkoperator_0`):

```
atlas migrate apply --dir "file://so/ent/migrate/migrations" --url "postgresql://127.0.0.1:5432/sparkoperator_0?sslmode=disable"
```

- Commit the migration files, and submit a PR.

If you are adding atlas migrations for the first time to an existing DB, you will need to run the migration command with the `--baseline` flag.

```
atlas migrate apply --dir "file://so/ent/migrate/migrations" --url "postgresql://127.0.0.1:5432/sparkoperator_0?sslmode=disable" --baseline 20250228224813
```

## VSCode

If spark_frost.udl file has issue with VSCode, you can add the following to your settings.json file:

```
"files.associations": {
    "spark_frost.udl": "plaintext"
}
```

## Linting

Linting uses `golang-ci`, install it [according to your platform](https://golangci-lint.run/welcome/install/). Note that it is discouraged to install from sources via `go install`.

```
# MacOS
brew install golangci-lint

```

To run the linters, use

```
golangci-lint run
```

## Run tests

### Unit tests

In spark folder, run:

```
go test $(go list ./... | grep -v -E "so/grpc_test|so/tree")
```

## E2E tests

The E2E test environment can be run locally via `./run-everything.sh` or in minikube via `./scripts/local-test.sh` for hermetic testing.

### Prerequisites

#### Local Setup (`./run-everything.sh`)
```
brew install tmux
brew install sqlx-cli # required for LRC20 Node
brew install cargo # required for LRC20 Node
```

##### bitcoind

See bitcoin section above.

##### postgres

A local version `postgres` with access for your local user.
If you have a fresh installation of `postgres`, you may need to add your user yourself:

```
psql -U postgres -c "CREATE ROLE $USER WITH LOGIN SUPERUSER;"
```

You also need to enable TCP/IP connections to the database.
You might need to edit the following files found in your `postgres` data directory. If you installed `postgres` via homebrew, it is probably in `/usr/local/var/postgres`. If you can connect to the database via `psql`, you can find the data directory by running `psql -U postgres -c "SHOW data_directory;"`.

A sample `postgresql.conf`:

```
hba_file = './pg_hba.conf'
ident_file = './pg_ident.conf'
listen_addresses = '*'
log_destination = 'stderr'
log_line_prefix = '[%p] '
port = 5432
```

A sample `pg_hba.conf`:

```
#type  database  user  address       method
local   all       all                trust
host    all       all   127.0.0.1/32 trust
host    all       all   ::1/128      trust
```

#### Hermetic/Minikube Setup (`./scripts/local-test.sh`)

##### minikube

See: [ops/minikube/README.md](https://github.com/lightsparkdev/ops/blob/main/minikube/README.md)

Please run: ops/minikube/setup.sh

### Running tests

All E2E tests live in the spark/so/grpc_test folder.

In the root folder, run:


```
# Local environment

./run-everything.sh
```

OR

```
# Hermetic/Minikube environment
#
# Env variables:
# RESET_DBS={default:true}      - resets the operator databases and bitcoin blockchain
# USE_DEV_SPARK={default:false} - use the dev spark image built into the minikube container cluster
#                                 (rebuild the the image with ./scripts/build.sh)
# SPARK_TAG={default:latest}    - the image tag to use for both Spark operator and signer
# LRC20_TAG={default:latest}    - the image tag to use for LRC20

./scripts/local-test.sh

# CTR-C when done to remove shut down port forwarding
```

Then in the spark folder:

```
go test -failfast=false -p=2 ./so/grpc_test/...
```

#### Troubleshooting

1. For local testing, operator (go) and signer (rust) logs are found in `_data/run_X/logs`. For minikube, logs are found via kubernetes.
2. If you don't want to deal with `tmux` commands yourself, you can easily interact with tmux using the `iterm2` GUI and tmux control-center.
   From within `iterm2`, you can run:

`tmux -CC attach -t operator`

3. The first time you run `run-everything.sh` it will take a while to start up. You might actually need to run it a couple of times for everything to work properly. Attach to the `operator` session and check out the logs.
