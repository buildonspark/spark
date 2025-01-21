# spark-go

## Setup

Protobuf needs to be installed in order to build the project.

```
brew install protobuf
```

To run the run-everything.sh script, you need to have tmux installed.

```
brew install tmux
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

## Run tests

### Unit tests

In spark folder, run:

```
go test $(go list ./... | grep -v -E "so/grpc_test|so/tree")
```

### E2E tests

All E2E tests live in the spark/so/grpc_test folder.

In the root folder, run:

```
./run-everything.sh
```

Then in the spark folder:

```
go test ./so/grpc_test/...
```
