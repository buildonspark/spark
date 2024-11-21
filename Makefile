# Directories
PROTO_DIR := protos
OUT_DIR := spark/proto

# Find all proto files
PROTO_FILES := $(wildcard $(PROTO_DIR)/*.proto)

# Generate output file paths
GO_OUT := $(patsubst $(PROTO_DIR)/%.proto,$(OUT_DIR)/%.pb.go,$(PROTO_FILES))

.PHONY: all clean ent proto

all: proto ent

proto: $(GO_OUT)

# Rule to compile .proto files to Go
$(OUT_DIR)/%.pb.go: $(PROTO_DIR)/%.proto
	@mkdir -p $(OUT_DIR)
	protoc --go_out=$(OUT_DIR) \
		--go_opt=paths=source_relative \
		--proto_path=$(PROTO_DIR) \
		--go-grpc_out=$(OUT_DIR) \
		--go-grpc_opt=paths=source_relative \
		$<

clean:
	rm -rf $(OUT_DIR)/*.pb.go

ent:
	cd spark && go generate ./so/ent
