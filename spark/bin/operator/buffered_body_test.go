package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	sparkgrpc "github.com/lightsparkdev/spark/so/grpc"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// trackingCloser records whether Close was called and lets us inject a close error.
type trackingCloser struct {
	io.Reader
	closed   bool
	closeErr error
}

func (c *trackingCloser) Close() error {
	c.closed = true
	return c.closeErr
}

func newBody(content string) *trackingCloser {
	return &trackingCloser{Reader: strings.NewReader(content)}
}

func TestBufferedBodyReadsFullContentAcrossSmallReads(t *testing.T) {
	const content = "hello buffered world"
	body := NewBufferedBody(newBody(content))

	// Read one byte at a time to exercise the Position bookkeeping.
	var readBytes []byte
	buf := make([]byte, 1)
	for {
		n, err := body.Read(buf)
		readBytes = append(readBytes, buf[:n]...)
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err)
	}

	assert.Equal(t, content, string(readBytes))
}

func TestBufferedBodyReadAllReturnsEntireBody(t *testing.T) {
	const content = "the entire body in one shot"
	body := NewBufferedBody(newBody(content))

	readBytes, err := io.ReadAll(body)
	require.NoError(t, err)
	assert.Equal(t, content, string(readBytes))
}

func TestBufferedBodyEmptyBodyReturnsEOF(t *testing.T) {
	body := NewBufferedBody(newBody(""))

	n, err := body.Read(make([]byte, 8))
	require.ErrorIs(t, err, io.EOF)
	assert.Zero(t, n, "expected no bytes from an empty body")
}

func TestBufferedBodyCloseDelegatesToUnderlyingReader(t *testing.T) {
	closer := newBody("data")
	body := NewBufferedBody(closer)

	require.NoError(t, body.Close())
	assert.True(t, closer.closed, "expected Close to be delegated to the underlying reader")
}

func TestBufferedBodyCloseSurfacesUnderlyingError(t *testing.T) {
	closer := &trackingCloser{Reader: strings.NewReader("data"), closeErr: fmt.Errorf("boom")}
	body := NewBufferedBody(closer)

	assert.Error(t, body.Close(), "expected Close to surface the underlying close error")
}

func TestBufferedBodyWithMaxBytesReaderUnderLimitSucceeds(t *testing.T) {
	const limit = 1024
	content := bytes.Repeat([]byte("a"), limit)
	body := NewBufferedBody(http.MaxBytesReader(nil, io.NopCloser(bytes.NewReader(content)), limit))

	readBytes, err := io.ReadAll(body)
	require.NoError(t, err, "reading a body within the limit should succeed")
	assert.Len(t, readBytes, limit)
}

// A body exceeding the limit must surface an error rather than buffering unbounded memory, and must not hand back the prefix.
func TestBufferedBodyWithMaxBytesReaderOverLimitErrors(t *testing.T) {
	const limit = 1024
	content := bytes.Repeat([]byte("a"), limit+1)
	body := NewBufferedBody(http.MaxBytesReader(nil, io.NopCloser(bytes.NewReader(content)), limit))

	readBytes, err := io.ReadAll(body)
	require.Error(t, err, "expected an error reading a body that exceeds the MaxBytesReader limit")
	assert.Empty(t, readBytes, "a failed read must not yield any of the truncated prefix")
}

// A read error must be sticky.
func TestBufferedBodyReadErrorIsSticky(t *testing.T) {
	const limit = 16
	content := bytes.Repeat([]byte("a"), limit*4)
	body := NewBufferedBody(http.MaxBytesReader(nil, io.NopCloser(bytes.NewReader(content)), limit))

	buf := make([]byte, 4)

	n1, err1 := body.Read(buf)
	require.Error(t, err1, "the over-limit read must surface an error")
	require.NotErrorIs(t, err1, io.EOF, "a failed read must never surface as a clean EOF")
	assert.Zero(t, n1, "the erroring read must not return any truncated prefix bytes")

	n2, err2 := body.Read(buf)
	assert.Equal(t, err1, err2, "read error must be sticky across calls")
	assert.Zero(t, n2, "a sticky-error read must keep returning zero bytes")
}

// Every request that passes the proto-size check should also fit under the HTTP body buffering cap.
func TestMaxHTTPBodySizeExceedsMaxRequestSize(t *testing.T) {
	assert.Greater(t, sparkgrpc.MaxHTTPBodySize, sparkgrpc.MaxRequestSize, "MaxHTTPBodySize must exceed MaxRequestSize")
}
