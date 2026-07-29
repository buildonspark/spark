package transfer

import (
	"encoding/asn1"
	"encoding/hex"
	"fmt"
	"math/big"
	"testing"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/btcnetwork"
	"github.com/lightsparkdev/spark/common/keys"
	"github.com/lightsparkdev/spark/proto/spark"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

const fixtureTransferID = "01890f5e-45a3-7f2c-8a2b-0242ac120002"

// senderSpec is one sender package's worth of intent: which leaves it moves, to whom, and for how
// much. The fixture turns these into a signed request plus the matching server-side leaf values.
type senderSpec struct {
	key   keys.Private
	leafs []leafSpec
}

type leafSpec struct {
	id       string
	receiver keys.Public
	sats     uint64
}

// bindingFixture builds requests that bind cleanly, so each test can mutate exactly one thing and
// attribute the failure to it. Signing happens in request(), after mutations, because every
// manifest change moves the hash.
type bindingFixture struct {
	t        *testing.T
	manifest *spark.TransferManifest
	senders  []senderSpec
	expiry   *timestamppb.Timestamp

	// signWith overrides the signing key for the sender at this index.
	signWith map[int]keys.Private
	// omitSignature drops the signature for the sender at this index.
	omitSignature map[int]bool
	// mangleSignature rewrites a sender's signature bytes after signing.
	mangleSignature map[int]func([]byte) []byte
	// extraLeafSats adds server-side leaves no package routes.
	extraLeafSats map[string]uint64
	// dropLeafSats removes a leaf's server-side value while leaving it routed.
	dropLeafSats map[string]bool
	leafOwner    map[string]keys.Public
	network      btcnetwork.Network
	// omitMultiSenderExpiry keeps the expiry unset even with several senders, which the binding
	// otherwise refuses — only the test for that refusal wants it.
	omitMultiSenderExpiry bool
}

func newBindingFixture(t *testing.T) *bindingFixture {
	t.Helper()
	sender := keys.GeneratePrivateKey()
	receiverA := keys.GeneratePrivateKey().Public()
	receiverB := keys.GeneratePrivateKey().Public()

	return &bindingFixture{
		t: t,
		manifest: &spark.TransferManifest{
			Version:    1,
			TransferId: fixtureTransferID,
			Network:    spark.Network_REGTEST,
			Edges: []*spark.ManifestEdge{
				edge(sender.Public(), receiverA, 600),
				edge(sender.Public(), receiverB, 400),
			},
		},
		senders: []senderSpec{{
			key: sender,
			leafs: []leafSpec{
				{id: "leaf-a", receiver: receiverA, sats: 600},
				{id: "leaf-b", receiver: receiverB, sats: 400},
			},
		}},
		network:         btcnetwork.Regtest,
		leafOwner:       map[string]keys.Public{},
		signWith:        map[int]keys.Private{},
		omitSignature:   map[int]bool{},
		mangleSignature: map[int]func([]byte) []byte{},
		extraLeafSats:   map[string]uint64{},
		dropLeafSats:    map[string]bool{},
	}
}

func edge(sender, receiver keys.Public, sats uint64) *spark.ManifestEdge {
	return &spark.ManifestEdge{
		SenderIdentityPublicKey:   sender.Serialize(),
		ReceiverIdentityPublicKey: receiver.Serialize(),
		Amount:                    &spark.ManifestAmount{Amount: &spark.ManifestAmount_Sats{Sats: sats}},
	}
}

func (f *bindingFixture) request() *spark.StartTransferV3Request {
	f.t.Helper()
	if f.expiry == nil && len(f.senders) > 1 && !f.omitMultiSenderExpiry {
		f.expiry = timestamppb.New(time.Unix(1900000000, 0))
	}
	f.manifest.TransferExpiryTime = f.expiry

	hash, err := common.HashTransferManifest(f.manifest)
	require.NoError(f.t, err, "fixture manifest must be hashable")

	pkgs := make([]*spark.SenderTransferPackage, 0, len(f.senders))
	for i, sender := range f.senders {
		receivers := make(map[string][]byte, len(sender.leafs))
		for _, leaf := range sender.leafs {
			receivers[leaf.id] = leaf.receiver.Serialize()
		}

		signingKey := sender.key
		if override, ok := f.signWith[i]; ok {
			signingKey = override
		}
		signature := ecdsa.Sign(signingKey.ToBTCEC(), hash).Serialize()
		if mangle, ok := f.mangleSignature[i]; ok {
			signature = mangle(signature)
		}
		if f.omitSignature[i] {
			signature = nil
		}

		pkgs = append(pkgs, &spark.SenderTransferPackage{
			OwnerIdentityPublicKey:     sender.key.Public().Serialize(),
			ReceiverIdentityPublicKeys: receivers,
			ManifestHashSignature:      signature,
		})
	}

	return &spark.StartTransferV3Request{
		TransferId:       fixtureTransferID,
		SenderPackages:   pkgs,
		ExpiryTime:       f.expiry,
		TransferManifest: f.manifest,
	}
}

func (f *bindingFixture) leafRecords() map[string]ExecutedLeaf {
	records := map[string]ExecutedLeaf{}
	for _, sender := range f.senders {
		for _, leaf := range sender.leafs {
			if !f.dropLeafSats[leaf.id] {
				owner := sender.key.Public()
				if override, ok := f.leafOwner[leaf.id]; ok {
					owner = override
				}
				records[leaf.id] = ExecutedLeaf{Owner: owner, ValueSats: leaf.sats}
			}
		}
	}
	for id, sats := range f.extraLeafSats {
		records[id] = ExecutedLeaf{Owner: f.senders[0].key.Public(), ValueSats: sats}
	}
	return records
}

func (f *bindingFixture) bind() error {
	return BindManifest(f.request(), f.network, f.leafRecords())
}

func TestBindManifestAcceptsAnExactlyCoveredTransfer(t *testing.T) {
	require.NoError(t, newBindingFixture(t).bind())
}

// Multi-sender is the shape MIMO needs, and the per-(sender, receiver) check has to hold there
// before any multi-sender flow exists to exercise it.
func TestBindManifestAcceptsMultipleSenders(t *testing.T) {
	f := newBindingFixture(t)
	senderB := keys.GeneratePrivateKey()
	receiverC := keys.GeneratePrivateKey().Public()

	f.senders = append(f.senders, senderSpec{
		key:   senderB,
		leafs: []leafSpec{{id: "leaf-c", receiver: receiverC, sats: 250}},
	})
	f.manifest.Edges = append(f.manifest.Edges, edge(senderB.Public(), receiverC, 250))

	require.NoError(t, f.bind())
}

// Two senders paying one receiver is the only shape where the sender half of the edge key does
// any work: with a receiver per sender, a sender-blind implementation binds identically.
func TestBindManifestAttributesValueToTheOwningSender(t *testing.T) {
	shared := func(t *testing.T) (*bindingFixture, keys.Public, keys.Public) {
		t.Helper()
		f := newBindingFixture(t)
		senderA := f.senders[0].key
		senderB := keys.GeneratePrivateKey()
		receiver := f.senders[0].leafs[0].receiver

		f.senders = []senderSpec{
			{key: senderA, leafs: []leafSpec{{id: "leaf-a", receiver: receiver, sats: 600}}},
			{key: senderB, leafs: []leafSpec{{id: "leaf-b", receiver: receiver, sats: 400}}},
		}
		return f, senderA.Public(), senderB.Public()
	}

	t.Run("attribution matching the manifest", func(t *testing.T) {
		f, senderA, senderB := shared(t)
		f.manifest.Edges = []*spark.ManifestEdge{
			edge(senderA, f.senders[0].leafs[0].receiver, 600),
			edge(senderB, f.senders[1].leafs[0].receiver, 400),
		}

		require.NoError(t, f.bind())
	})

	// The totals still sum to 1000 across both senders, so only per-sender attribution rejects it.
	t.Run("amounts swapped between the senders", func(t *testing.T) {
		f, senderA, senderB := shared(t)
		f.manifest.Edges = []*spark.ManifestEdge{
			edge(senderA, f.senders[0].leafs[0].receiver, 400),
			edge(senderB, f.senders[1].leafs[0].receiver, 600),
		}

		require.ErrorIs(t, f.bind(), ErrManifestAmountMismatch)
	})
}

// Real manifests always carry these, and they legitimately hold bps amounts and recipients that
// appear in no edge — so the exact-cover check must not reach into them.
func TestBindManifestIgnoresPricedFields(t *testing.T) {
	f := newBindingFixture(t)
	f.manifest.QuoteExpiryTime = timestamppb.New(time.Unix(1900000000, 0))
	f.manifest.Fees = []*spark.FeeComponent{
		{Source: spark.FeeSource_FEE_SOURCE_BASE, Amount: satsOf(7)},
		{
			Source:                     spark.FeeSource_FEE_SOURCE_PARTNER_MARKUP,
			Role:                       spark.FeeRole_FEE_ROLE_AFFILIATE,
			Amount:                     &spark.ManifestAmount{Amount: &spark.ManifestAmount_Bps{Bps: 150}},
			RecipientIdentityPublicKey: keys.GeneratePrivateKey().Public().Serialize(),
		},
	}

	require.NoError(t, f.bind())
}

// A dual-role key — destination and fee recipient — gets one edge for its total, not one per
// role. Accepting the split form would give a single movement two signable representations.
func TestBindManifestRejectsRepeatedEdgesForOnePair(t *testing.T) {
	f := newBindingFixture(t)
	sender := f.senders[0].key.Public()
	receiverA := f.senders[0].leafs[0].receiver

	// Splitting 600 into 200+400 covers the executed leaves exactly, so only the one-edge-per-pair
	// rule can reject it.
	f.manifest.Edges = []*spark.ManifestEdge{
		edge(sender, receiverA, 200),
		edge(sender, receiverA, 400),
		edge(sender, f.senders[0].leafs[1].receiver, 400),
	}

	require.ErrorIs(t, f.bind(), ErrManifestDuplicateEdge)
}

// The collapsed form of the same movement is what a correct manifest carries.
func TestBindManifestAcceptsTheCollapsedEdgeForADualRoleKey(t *testing.T) {
	f := newBindingFixture(t)
	sender := f.senders[0].key.Public()

	f.manifest.Edges = []*spark.ManifestEdge{
		edge(sender, f.senders[0].leafs[0].receiver, 600),
		edge(sender, f.senders[0].leafs[1].receiver, 400),
	}

	require.NoError(t, f.bind())
}

func TestBindManifestExactCoverViolations(t *testing.T) {
	tests := map[string]struct {
		mutate  func(*bindingFixture)
		wantErr error
	}{
		"declared edge that moves no leaves": {
			mutate: func(f *bindingFixture) {
				orphan := keys.GeneratePrivateKey().Public()
				f.manifest.Edges = append(f.manifest.Edges, edge(f.senders[0].key.Public(), orphan, 50))
			},
			wantErr: ErrManifestEdgeNotRealized,
		},
		// Every declared edge stays realized here, so only the extra pair can be what fails —
		// otherwise this would also trip the edge-not-realized check and prove nothing.
		"leaves routed to a receiver no edge names": {
			mutate: func(f *bindingFixture) {
				stray := keys.GeneratePrivateKey().Public()
				f.senders[0].leafs = append(f.senders[0].leafs, leafSpec{id: "leaf-stray", receiver: stray, sats: 75})
			},
			wantErr: ErrManifestUnlistedTransfer,
		},
		"edge amount above what the leaves move": {
			mutate:  func(f *bindingFixture) { f.manifest.Edges[0].Amount = satsOf(601) },
			wantErr: ErrManifestAmountMismatch,
		},
		"edge amount below what the leaves move": {
			mutate:  func(f *bindingFixture) { f.manifest.Edges[0].Amount = satsOf(599) },
			wantErr: ErrManifestAmountMismatch,
		},
		"server-side leaf no package routes": {
			mutate:  func(f *bindingFixture) { f.extraLeafSats["leaf-stray"] = 25 },
			wantErr: ErrManifestLeafNotRouted,
		},
		"routed leaf with no server-side value": {
			mutate:  func(f *bindingFixture) { f.dropLeafSats["leaf-b"] = true },
			wantErr: ErrManifestUnknownLeaf,
		},
		"edge denominated in bps": {
			mutate: func(f *bindingFixture) {
				f.manifest.Edges[0].Amount = &spark.ManifestAmount{Amount: &spark.ManifestAmount_Bps{Bps: 100}}
			},
			wantErr: ErrManifestNonSatsEdge,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			f := newBindingFixture(t)
			test.mutate(f)
			require.ErrorIs(t, f.bind(), test.wantErr)
		})
	}
}

func satsOf(sats uint64) *spark.ManifestAmount {
	return &spark.ManifestAmount{Amount: &spark.ManifestAmount_Sats{Sats: sats}}
}

func TestBindManifestSignatureFailures(t *testing.T) {
	tests := map[string]struct {
		mutate  func(*bindingFixture)
		wantErr error
	}{
		"signed by a key that is not the sender": {
			mutate:  func(f *bindingFixture) { f.signWith[0] = keys.GeneratePrivateKey() },
			wantErr: ErrManifestInvalidSignature,
		},
		"no signature at all": {
			mutate:  func(f *bindingFixture) { f.omitSignature[0] = true },
			wantErr: ErrManifestMissingSignature,
		},
		"malformed signature bytes": {
			mutate: func(f *bindingFixture) {
				f.mangleSignature[0] = func(sig []byte) []byte { return []byte{0x01, 0x02, 0x03} }
			},
			wantErr: ErrManifestInvalidSignature,
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			f := newBindingFixture(t)
			test.mutate(f)
			require.ErrorIs(t, f.bind(), test.wantErr)
		})
	}
}

// The headline property, and the only case here where the signer is right and the document is not.
func TestBindManifestRejectsAManifestSwappedAfterSigning(t *testing.T) {
	f := newBindingFixture(t)
	req := f.request()

	tampered := proto.Clone(f.manifest).(*spark.TransferManifest)
	tampered.Edges[0].Amount = satsOf(900)
	tampered.Edges[1].Amount = satsOf(100)
	req.TransferManifest = tampered

	// The totals still cover the executed leaves exactly, so only the signature can reject this.
	require.ErrorIs(t, BindManifest(req, f.network, f.leafRecords()), ErrManifestInvalidSignature)
}

func TestBindManifestRejectsANetworkMismatch(t *testing.T) {
	f := newBindingFixture(t)
	f.network = btcnetwork.Mainnet

	require.ErrorIs(t, f.bind(), ErrManifestNetworkMismatch)
}

// Mutated post-signing because hashing refuses an unspecified network. Asserting a network error
// rather than the hash error pins that the gate runs ahead of the digest backstop — and its own
// sentinel, because an unusable network is not the same fault as one that simply disagrees.
func TestBindManifestRejectsAnUnknownNetwork(t *testing.T) {
	f := newBindingFixture(t)
	req := f.request()
	req.TransferManifest.Network = spark.Network_UNSPECIFIED

	err := BindManifest(req, f.network, f.leafRecords())
	require.ErrorIs(t, err, ErrManifestUnknownNetwork)
	require.NotErrorIs(t, err, ErrManifestNetworkMismatch)
}

func TestBindManifestRejectsALeafRoutedByANonOwner(t *testing.T) {
	f := newBindingFixture(t)
	f.leafOwner["leaf-a"] = keys.GeneratePrivateKey().Public()

	err := f.bind()

	require.ErrorIs(t, err, ErrManifestLeafOwnerMismatch)
	// The true owner is an operator-side fact; echoing it would make this an ownership oracle.
	require.NotContains(t, err.Error(), hex.EncodeToString(f.leafOwner["leaf-a"].Serialize()))
}

// One leaf counted under two packages would be paid once but credited to two edges.
func TestBindManifestRejectsALeafRoutedByTwoPackages(t *testing.T) {
	f := newBindingFixture(t)
	senderA := f.senders[0].key
	receiver := f.senders[0].leafs[0].receiver

	f.senders = []senderSpec{
		{key: senderA, leafs: []leafSpec{{id: "leaf-a", receiver: receiver, sats: 600}}},
		{key: keys.GeneratePrivateKey(), leafs: []leafSpec{{id: "leaf-a", receiver: receiver, sats: 600}}},
	}
	// Owned by the first package, so the second one trips the duplicate guard rather than the
	// owner guard — otherwise this would not test what it names.
	f.leafOwner["leaf-a"] = senderA.Public()

	require.ErrorIs(t, f.bind(), ErrDuplicateLeafID)
}

func TestBindManifestRejectsOversizedRequests(t *testing.T) {
	t.Run("too many sender packages", func(t *testing.T) {
		f := newBindingFixture(t)
		req := f.request()
		pkg := req.GetSenderPackages()[0]
		for len(req.GetSenderPackages()) <= MaxManifestSenderPackages {
			req.SenderPackages = append(req.SenderPackages, pkg)
		}

		require.ErrorIs(t, BindManifest(req, f.network, f.leafRecords()), ErrManifestTooLarge)
	})

	t.Run("too many edges", func(t *testing.T) {
		f := newBindingFixture(t)
		req := f.request()
		edges := req.GetTransferManifest().GetEdges()
		for len(edges) <= MaxManifestEdges {
			edges = append(edges, edges[0])
		}
		req.TransferManifest.Edges = edges

		require.ErrorIs(t, BindManifest(req, f.network, f.leafRecords()), ErrManifestTooLarge)
	})

	t.Run("too many routed leaves", func(t *testing.T) {
		f := newBindingFixture(t)
		req := f.request()
		routed := req.GetSenderPackages()[0].GetReceiverIdentityPublicKeys()
		receiver := f.senders[0].leafs[0].receiver.Serialize()
		for i := range MaxManifestRoutedLeaves {
			routed[fmt.Sprintf("filler-%d", i)] = receiver
		}

		require.ErrorIs(t, BindManifest(req, f.network, f.leafRecords()), ErrManifestTooLarge)
	})

	t.Run("too many fees", func(t *testing.T) {
		f := newBindingFixture(t)
		req := f.request()
		fees := make([]*spark.FeeComponent, 0, MaxManifestFees+1)
		for range MaxManifestFees + 1 {
			fees = append(fees, &spark.FeeComponent{Source: spark.FeeSource_FEE_SOURCE_BASE, Amount: satsOf(1)})
		}
		req.TransferManifest.Fees = fees

		require.ErrorIs(t, BindManifest(req, f.network, f.leafRecords()), ErrManifestTooLarge)
	})

	// Rejected before the digest, so an oversized request never buys the hashing work either.
	t.Run("size is checked ahead of hashing", func(t *testing.T) {
		f := newBindingFixture(t)
		req := f.request()
		req.TransferManifest.Version = 99
		pkg := req.GetSenderPackages()[0]
		for len(req.GetSenderPackages()) <= MaxManifestSenderPackages {
			req.SenderPackages = append(req.SenderPackages, pkg)
		}

		require.ErrorIs(t, BindManifest(req, f.network, f.leafRecords()), ErrManifestTooLarge)
	})
}

// The executed side is the only one that accumulates — many leaves per pair — so it is the only
// side that can wrap. Unreachable once leaf values are real rows, but kept honest.
func TestBindManifestRejectsOverflowingTotals(t *testing.T) {
	f := newBindingFixture(t)
	receiver := f.senders[0].leafs[0].receiver
	f.senders[0].leafs = []leafSpec{
		{id: "leaf-a", receiver: receiver, sats: 1 << 63},
		{id: "leaf-b", receiver: receiver, sats: 1 << 63},
	}
	f.manifest.Edges = []*spark.ManifestEdge{edge(f.senders[0].key.Public(), receiver, 1000)}

	require.ErrorIs(t, f.bind(), ErrManifestTotalOverflow)
}

// Sole-sender is the documented shape for omitting it; across senders it would let whoever
// assembles the request pick timing none of the signers agreed to.
func TestBindManifestRejectsAnUnsignedExpiryAcrossSenders(t *testing.T) {
	f := newBindingFixture(t)
	f.omitMultiSenderExpiry = true
	senderB := keys.GeneratePrivateKey()
	receiverC := keys.GeneratePrivateKey().Public()
	f.senders = append(f.senders, senderSpec{
		key:   senderB,
		leafs: []leafSpec{{id: "leaf-c", receiver: receiverC, sats: 250}},
	})
	f.manifest.Edges = append(f.manifest.Edges, edge(senderB.Public(), receiverC, 250))

	require.ErrorIs(t, f.bind(), ErrManifestExpiryUnsigned)
}

// Sole-sender is the case the omission exists for, and it must keep working.
func TestBindManifestAllowsAnOmittedExpiryForASoleSender(t *testing.T) {
	f := newBindingFixture(t)
	f.expiry = nil

	require.NoError(t, f.bind())
}

func TestBindManifestRejectsUnparseableKeys(t *testing.T) {
	notOnCurve := make([]byte, 33)
	notOnCurve[0] = 0x02

	t.Run("sender identity key", func(t *testing.T) {
		f := newBindingFixture(t)
		req := f.request()
		req.SenderPackages[0].OwnerIdentityPublicKey = notOnCurve

		require.ErrorIs(t, BindManifest(req, f.network, f.leafRecords()), ErrManifestInvalidSender)
	})

	t.Run("per-leaf receiver key", func(t *testing.T) {
		f := newBindingFixture(t)
		req := f.request()
		req.GetSenderPackages()[0].ReceiverIdentityPublicKeys["leaf-a"] = notOnCurve

		err := BindManifest(req, f.network, f.leafRecords())
		require.ErrorIs(t, err, ErrManifestInvalidReceiver)
		assert.NotContains(t, err.Error(), "sender identity", "a bad receiver key must not read as a sender problem")
	})

	// Mutated before signing: signatures are verified ahead of edge parsing, so an edge key
	// swapped afterwards would be rejected as a bad signature and never reach the parse.
	t.Run("manifest edge receiver key", func(t *testing.T) {
		f := newBindingFixture(t)
		f.manifest.Edges[0].ReceiverIdentityPublicKey = notOnCurve

		err := f.bind()
		require.ErrorIs(t, err, ErrManifestInvalidReceiver)
		assert.NotContains(t, err.Error(), "sender identity", "a bad receiver key must not read as a sender problem")
	})

	t.Run("manifest edge sender key", func(t *testing.T) {
		f := newBindingFixture(t)
		f.manifest.Edges[0].SenderIdentityPublicKey = notOnCurve

		require.ErrorIs(t, f.bind(), ErrManifestInvalidSender)
	})
}

// A high-S signature is a second valid encoding of an accepted one. Rejecting it keeps the
// signature a unique artifact, so a bound manifest cannot be re-presented in a malleated form.
func TestBindManifestRejectsHighSSignature(t *testing.T) {
	// Re-encoding through the same ASN.1 path without flipping S must still pass, which is what
	// proves the rejection below is about high-S and not about the re-encoding itself.
	t.Run("control: re-encoded low-S signature is accepted", func(t *testing.T) {
		f := newBindingFixture(t)
		f.mangleSignature[0] = func(sig []byte) []byte { return reencodeDER(t, sig, false) }
		require.NoError(t, f.bind())
	})

	t.Run("high-S signature is rejected", func(t *testing.T) {
		f := newBindingFixture(t)
		f.mangleSignature[0] = func(sig []byte) []byte { return reencodeDER(t, sig, true) }
		require.ErrorIs(t, f.bind(), ErrManifestInvalidSignature)
	})
}

// reencodeDER round-trips a DER ECDSA signature, optionally replacing S with N-S. The signing
// path always normalizes to low-S, so a high-S encoding has to be built by hand.
func reencodeDER(t *testing.T, der []byte, flipS bool) []byte {
	t.Helper()
	var parsed struct{ R, S *big.Int }
	rest, err := asn1.Unmarshal(der, &parsed)
	require.NoError(t, err)
	require.Empty(t, rest)

	if flipS {
		order := secp256k1.S256().N
		parsed.S = new(big.Int).Sub(order, parsed.S)
	}

	out, err := asn1.Marshal(parsed)
	require.NoError(t, err)
	return out
}

func TestBindManifestRejectsMismatchedTransferID(t *testing.T) {
	f := newBindingFixture(t)
	f.manifest.TransferId = "01890f5e-45a3-7f2c-8a2b-0242ac120099"

	require.ErrorIs(t, f.bind(), ErrManifestTransferIDMismatch)
}

func TestBindManifestRejectsAMissingManifest(t *testing.T) {
	f := newBindingFixture(t)
	req := f.request()
	req.TransferManifest = nil

	require.ErrorIs(t, BindManifest(req, f.network, map[string]ExecutedLeaf{}), ErrManifestMissing)
}

func TestBindManifestRejectsDuplicateSenders(t *testing.T) {
	f := newBindingFixture(t)
	// Same identity key in two packages: the proto documents one entry per sender, and allowing a
	// repeat would let one signature stand in for leaves counted under a second package.
	f.senders = append(f.senders, senderSpec{
		key:   f.senders[0].key,
		leafs: []leafSpec{{id: "leaf-c", receiver: f.senders[0].leafs[0].receiver, sats: 10}},
	})

	require.ErrorIs(t, f.bind(), ErrManifestDuplicateSender)
}

func TestBindManifestExpiry(t *testing.T) {
	future := timestamppb.New(time.Unix(1900000000, 0))

	t.Run("manifest omits the expiry", func(t *testing.T) {
		f := newBindingFixture(t)
		f.expiry = nil

		require.NoError(t, f.bind())
	})

	// Deliberate asymmetry: a manifest that stays silent about expiry constrains nothing, so the
	// transfer may still set one. Pinned so tightening the nil case later is a conscious choice.
	t.Run("manifest omits the expiry the request sets", func(t *testing.T) {
		f := newBindingFixture(t)
		f.expiry = nil
		req := f.request()
		req.ExpiryTime = future

		require.NoError(t, BindManifest(req, f.network, f.leafRecords()))
	})

	// Pins the boundary itself: a whole-millisecond gap is a different signed instant, so widening
	// the comparison to seconds must not pass.
	t.Run("expiry differing by a whole millisecond", func(t *testing.T) {
		f := newBindingFixture(t)
		f.expiry = timestamppb.New(time.Unix(1900000000, 1_000_000))
		req := f.request()
		req.ExpiryTime = timestamppb.New(time.Unix(1900000000, 2_000_000))

		require.ErrorIs(t, BindManifest(req, f.network, f.leafRecords()), ErrManifestExpiryMismatch)
	})

	// The digest floors to whole milliseconds, so a sub-millisecond difference is authorized by
	// the same signature and must not be rejected.
	t.Run("expiry differing only below the signed precision", func(t *testing.T) {
		f := newBindingFixture(t)
		f.expiry = timestamppb.New(time.Unix(1900000000, 250000))
		req := f.request()
		req.ExpiryTime = timestamppb.New(time.Unix(1900000000, 750000))

		require.NoError(t, BindManifest(req, f.network, f.leafRecords()))
	})

	t.Run("manifest expiry matches the request", func(t *testing.T) {
		f := newBindingFixture(t)
		f.expiry = future

		require.NoError(t, f.bind())
	})

	t.Run("manifest expiry differs from the request", func(t *testing.T) {
		f := newBindingFixture(t)
		f.expiry = future
		req := f.request()
		req.ExpiryTime = timestamppb.New(time.Unix(1900000001, 0))

		require.ErrorIs(t, BindManifest(req, f.network, f.leafRecords()), ErrManifestExpiryMismatch)
	})

	t.Run("manifest sets an expiry the request omits", func(t *testing.T) {
		f := newBindingFixture(t)
		f.expiry = future
		req := f.request()
		req.ExpiryTime = nil

		require.ErrorIs(t, BindManifest(req, f.network, f.leafRecords()), ErrManifestExpiryMismatch)
	})
}

// An unhashable manifest must fail closed rather than bind against a digest nobody signed.
func TestBindManifestRejectsAnUnhashableManifest(t *testing.T) {
	f := newBindingFixture(t)
	req := f.request()
	req.TransferManifest.Version = 99

	require.ErrorIs(t, BindManifest(req, f.network, f.leafRecords()), ErrManifestNotHashable)
}
