package main

import (
	"encoding/base64"
	"encoding/binary"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// parseGrpcWebTrailerFrame decodes a single gRPC-web trailer frame and returns its trailer block.
func parseGrpcWebTrailerFrame(t *testing.T, frame []byte) string {
	t.Helper()
	require.GreaterOrEqual(t, len(frame), 5, "frame must contain the 1-byte flag and 4-byte length prefix")
	assert.Equal(t, byte(1<<7), frame[0], "high bit must be set to mark a trailer frame")
	length := binary.BigEndian.Uint32(frame[1:5])
	require.Len(t, frame[5:], int(length), "declared trailer length must match the payload")
	return string(frame[5:])
}

func TestWriteGrpcResourceExhaustedGrpcWeb(t *testing.T) {
	rec := httptest.NewRecorder()
	writeGrpcResourceExhausted(rec, "application/grpc-web+proto", "too big")

	assert.Equal(t, http.StatusOK, rec.Code, "gRPC status rides in the trailer, HTTP is always 200")
	assert.Equal(t, "application/grpc-web+proto", rec.Header().Get("Content-Type"))

	trailers := parseGrpcWebTrailerFrame(t, rec.Body.Bytes())
	assert.Contains(t, trailers, "grpc-status:8")
	assert.Contains(t, trailers, "grpc-message:too big")
}

func TestWriteGrpcResourceExhaustedGrpcWebTextIsBase64(t *testing.T) {
	rec := httptest.NewRecorder()
	writeGrpcResourceExhausted(rec, "application/grpc-web-text", "too big")

	assert.Equal(t, http.StatusOK, rec.Code)
	decoded, err := base64.StdEncoding.DecodeString(rec.Body.String())
	require.NoError(t, err, "grpc-web-text response body must be base64-encoded")

	trailers := parseGrpcWebTrailerFrame(t, decoded)
	assert.Contains(t, trailers, "grpc-status:8")
}

func TestWriteGrpcResourceExhaustedNativeGrpcIsTrailersOnly(t *testing.T) {
	rec := httptest.NewRecorder()
	writeGrpcResourceExhausted(rec, "application/grpc", "too big")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "application/grpc", rec.Header().Get("Content-Type"))
	assert.Equal(t, "8", rec.Header().Get("Grpc-Status"), "ResourceExhausted is code 8")
	assert.Equal(t, "too big", rec.Header().Get("Grpc-Message"))
	assert.Empty(t, rec.Body.Bytes(), "a Trailers-Only response carries no body")
}
