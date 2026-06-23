package brontide

import (
	"bytes"
	"math"
	"net"
	"slices"
	"sync"
	"time"

	"github.com/lightningnetwork/lnd/brontide"
)

// wrappedConn is a net.Conn that applies brontide's AEAD record framing (Noise_XK ChaChaPoly) on top of an existing
// transport (for us, a TLS-protected net.Conn).
//
// We implement our own wrapper instead of using brontide.Conn directly because brontide.Conn's fields are unexported
// and its only public constructors are coupled to TCP. The framing logic is the same as brontide.Conn.{Read,Write}
// (brontide/conn.go in lnd v0.18.5-beta.rc1).
type wrappedConn struct {
	inner net.Conn
	noise *brontide.Machine

	readMu  sync.Mutex
	readBuf bytes.Buffer

	writeMu sync.Mutex
}

func newWrappedConn(inner net.Conn, m *brontide.Machine) *wrappedConn {
	return &wrappedConn{inner: inner, noise: m}
}

// Read decrypts the next inbound brontide record into an internal buffer if the buffer is empty, then serves bytes from the buffer.
func (c *wrappedConn) Read(b []byte) (int, error) {
	// io.Reader / net.Conn contract: Read on an empty buffer returns (0, nil) immediately, without blocking.
	// Without this guard, callers that probe for readability with a zero-length buffer would deadlock here because
	// ReadMessage below would block until peer data arrives.
	if len(b) == 0 {
		return 0, nil
	}

	c.readMu.Lock()
	defer c.readMu.Unlock()

	if c.readBuf.Len() == 0 {
		plaintext, err := c.noise.ReadMessage(c.inner)
		if err != nil {
			return 0, err
		}
		if _, err := c.readBuf.Write(plaintext); err != nil {
			return 0, err
		}
	}
	return c.readBuf.Read(b)
}

// Write encrypts b as one or more brontide AEAD records and flushes each to the underlying transport. Records carry a
// 16-bit length prefix, so payloads larger than 64KB-1 are split across records, matching brontide.Conn.Write.
func (c *wrappedConn) Write(b []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	bytesWritten := 0
	for chunk := range slices.Chunk(b, math.MaxUint16) {
		if err := c.noise.WriteMessage(chunk); err != nil {
			return bytesWritten, err
		}
		if _, err := c.noise.Flush(c.inner); err != nil {
			return bytesWritten, err
		}
		bytesWritten += len(chunk)
	}
	return bytesWritten, nil
}

func (c *wrappedConn) Close() error                      { return c.inner.Close() }
func (c *wrappedConn) LocalAddr() net.Addr               { return c.inner.LocalAddr() }
func (c *wrappedConn) RemoteAddr() net.Addr              { return c.inner.RemoteAddr() }
func (c *wrappedConn) SetDeadline(t time.Time) error     { return c.inner.SetDeadline(t) }
func (c *wrappedConn) SetReadDeadline(t time.Time) error { return c.inner.SetReadDeadline(t) }
func (c *wrappedConn) SetWriteDeadline(t time.Time) error {
	return c.inner.SetWriteDeadline(t)
}
