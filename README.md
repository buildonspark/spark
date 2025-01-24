# spark-go

## Setup

Protobuf needs to be installed in order to build the project.

```
brew install protobuf
```

Go protobuf tools need to be installed as well:
```
go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
```

## Generate proto files

After modifying the proto files, you can generate the Go files with the following command:

```
make
```

## Developer tips

If spark_frost.udl file has issue with VSCode, you can add the following to your settings.json file:

```
"files.associations": {
    "spark_frost.udl": "plaintext"
}
```

Linting uses `golint`, install it with:
```
go install golang.org/x/lint/golint@latest
```

## Run tests

### Unit tests

In spark folder, run:

```
go test $(go list ./... | grep -v -E "so/grpc_test|so/tree")
```

### E2E tests

#### Prerequisites

##### tmux
```brew install tmux```
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

##### cargo

```brew install cargo```

#### Running tests

All E2E tests live in the spark/so/grpc_test folder.

In the root folder, run:

```
./run-everything.sh
```

Then in the spark folder:

```
go test ./so/grpc_test/...
```

#### Troubleshooting

1. Operator (go) and signer (rust) logs are found in `_data/run_X/logs`.
2. If you don't want to deal with `tmux` commands yourself, you can easily interact with tmux using the `iterm2` GUI and tmux control-center.
From within `iterm2`, you can run:

```tmux -CC attach -t operator```

3. The first time you run `run-everything.sh` it will take a while to start up. You might actually need to run it a couple of times for everything to work properly. Attach to the `operator` session and check out the logs.
