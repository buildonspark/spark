package brontide

import (
	"fmt"
	"reflect"
	"unsafe"

	"github.com/btcsuite/btcd/btcec/v2"
	"github.com/lightningnetwork/lnd/brontide"
	"github.com/lightsparkdev/spark/common/keys"
)

// machineRemoteStaticOffset is the byte offset of the remote static public key field within brontide.Machine (via the
// embedded handshakeState). It's computed once at init() so the per-handshake cost is just a pointer load.
//
// This exists because brontide.Machine doesn't expose an accessor for the remote static key after a completed handshake.
// Only brontide.Conn.RemotePub does, and we can't build a brontide.Conn from outside the package, since its fields
// aren't exported and its constructors are TCP-coupled.
var machineRemoteStaticOffset uintptr

// init is meant as a mitigation against brontide internal drift. It verifies the field name, position, and type. If lnd
// ever changes the field, the binary fails to start with a clear error rather than silently misbehaving at runtime. This
// could be fixed upstream if we forked the package by adding Machine.RemoteStatic() in lnd and deleting this file.
func init() {
	mt := reflect.TypeFor[brontide.Machine]()

	hs, ok := mt.FieldByName("handshakeState")
	if !ok {
		panic("brontide internals changed: Machine.handshakeState not found; update spark/so/rpcauth/brontide accessor")
	}

	rs, ok := hs.Type.FieldByName("remoteStatic")
	if !ok {
		panic("brontide internals changed: handshakeState.remoteStatic not found; update spark/so/rpcauth/brontide accessor")
	}

	want := reflect.TypeFor[*btcec.PublicKey]()
	if rs.Type != want {
		panic(fmt.Sprintf("brontide internals changed: handshakeState.remoteStatic type is %v, want %v", rs.Type, want))
	}

	machineRemoteStaticOffset = hs.Offset + rs.Offset
}

// machineRemoteStatic returns the remote peer's static public key recovered from a completed brontide handshake. The
// boolean is false if the handshake has not yet produced a remote static (e.g. before RecvActThree on the responder
// side, or in any state on the initiator side where remoteStatic was passed in by the caller and is therefore already
// known to them).
func machineRemoteStatic(m *brontide.Machine) (keys.Public, bool) {
	p := unsafe.Add(unsafe.Pointer(m), machineRemoteStaticOffset)
	ptr := *(**btcec.PublicKey)(p)
	if ptr == nil {
		return keys.Public{}, false
	}
	return keys.PublicKeyFromKey(*ptr), true
}
