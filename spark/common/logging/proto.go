package logging

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

// In logstash we have a limit of 131KB (???) for the size of a message, so we don't want to be
// logging protos that are too big. So put a reasonable limit on this.
var maxBytes = 32 * 1024 // 32KB

var sensitiveFields = []string{"secret_share"}

// FormatProto formats a protobuf message in text format and returns it as a string.
// The name parameter is used to describe what kind of protobuf message it is (e.g., "transaction", "request", etc).
// The output includes partial objects and unknown fields for better debugging.
func FormatProto(name string, protoMsg proto.Message) string {
	if protoMsg == nil {
		return fmt.Sprintf("(%s: <nil>)", name)
	}

	// Use custom marshaler to handle binary data
	text, err := FormatProtoMessage(protoMsg)
	if err != nil {
		return fmt.Sprintf("(%s: <%v>)", name, err)
	}

	return fmt.Sprintf("(%s: %s)", name, text)
}

// FormatProtoMessage formats a protobuf message while redacting sensitive fields.
func FormatProtoMessage(msg proto.Message) (string, error) {
	// Alright, this is a super gross way to do this, but protojson doesn't give us any customization
	// in terms of how we want values rendered, so we need to take matters into our own hands...

	// Step 1: Marshal the protobuf message into protojson.
	opts := protojson.MarshalOptions{
		Multiline:         false,
		UseProtoNames:     true,
		AllowPartial:      true,
		EmitDefaultValues: true,
	}
	jsonBytes, err := opts.Marshal(msg)
	if err != nil {
		return "", fmt.Errorf("ERR_BAD_MSG")
	}

	// Step 2: Decode the JSON into a map[string]interface{}.
	var data map[string]any
	if err := json.Unmarshal(jsonBytes, &data); err != nil {
		return "", fmt.Errorf("ERR_NOT_JSON_OBJ")
	}

	// Step 3: Traverse the JSON and redact / format the fields.
	traverseAndFormatJSON(data)

	// Step 4: Marshal the updated map back to JSON and convert it to a string.
	output, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("ERR_BAD_JSON")
	}

	// We don't want to be logging values that are too big.
	if len(jsonBytes) > maxBytes {
		return "", fmt.Errorf("ERR_MSG_TOO_BIG")
	}

	return string(output), nil
}

func maybeConvertBase64ToHex(value string) string {
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return value
	}
	return "0x" + hex.EncodeToString(decoded)
}

// Given a JSON object (represented as a map), this function recursively traverses the object
// and does two things:
// 1. Redacts sensitive fields by replacing their values with "<REDACTED>".
// 2. Converts base64-encoded strings to hex format, unless they are in the sensitive fields list.
func traverseAndFormatJSON(data map[string]any) {
	for key, val := range data {
		switch v := val.(type) {
		case string:
			if slices.Contains(sensitiveFields, key) {
				data[key] = "<REDACTED>"
			} else {
				data[key] = maybeConvertBase64ToHex(v)
			}
		case map[string]any:
			traverseAndFormatJSON(v)
		case []any:
			for i, item := range v {
				switch elem := item.(type) {
				case map[string]any:
					traverseAndFormatJSON(elem)
				case string:
					v[i] = maybeConvertBase64ToHex(elem)
				}
			}
		}
	}
}
