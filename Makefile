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
		--validate_out="lang=go,paths=source_relative:$(dir $@)" \
		$<
# Default target
all: $(GO_OUT) copy-protos

# Clean target
clean:
	rm -rf spark/proto/*/*.pb.go
	rm -rf spark/proto/*/*.pb.validate.go

ent:
	cd spark && go generate ./...
	@echo "\n!!!!\nEnts generated. Remember to add migration changes with atlas! See README.md for more info.\n!!!!\n"

copy-protos:
	cp protos/common.proto signer/spark-frost/protos/
	cp protos/frost.proto signer/spark-frost/protos/
