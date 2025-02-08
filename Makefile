# Directory containing .proto files
PROTO_DIR := protos

# List of .proto files
PROTO_FILES := $(wildcard $(PROTO_DIR)/*.proto)

# Generate output file paths
GO_OUT := $(patsubst $(PROTO_DIR)/%.proto,spark/proto/%/%.pb.go,$(PROTO_FILES))

# Rule to compile .proto files to Go
spark/proto/%/%.pb.go: $(PROTO_DIR)/%.proto
	@echo "Compiling $< to $@"
	@mkdir -p $(dir $@)
	protoc --go_out=$(dir $@) \
		--go_opt=paths=source_relative \
		--proto_path=$(PROTO_DIR) \
		--go-grpc_out=$(dir $@) \
		--go-grpc_opt=paths=source_relative \
		$<

# Default target
all: $(GO_OUT) copy-protos

# Clean target
clean:
	rm -rf spark/proto/*/*.pb.go

ent:
	cd spark && go generate ./so/ent

copy-protos:
	cp protos/common.proto signer/spark-frost/protos/
	cp protos/frost.proto signer/spark-frost/protos/
