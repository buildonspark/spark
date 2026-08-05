package common

import (
	"testing"

	"github.com/lightsparkdev/spark/common/protohash"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func validManifest() *pb.TransferManifest {
	return &pb.TransferManifest{
		Version:    1,
		TransferId: "01890f5e-45a3-7f2c-8a2b-0242ac120002",
		Network:    pb.Network_REGTEST,
		Edges: []*pb.ManifestEdge{
			{
				SenderIdentityPublicKey:   testPubKey(0x02),
				ReceiverIdentityPublicKey: testPubKey(0x03),
				Amount:                    satsAmount(1000),
			},
		},
	}
}

func testPubKey(prefix byte) []byte {
	key := make([]byte, 33)
	key[0] = prefix
	key[32] = 0x01
	return key
}

func satsAmount(sats uint64) *pb.ManifestAmount {
	return &pb.ManifestAmount{Amount: &pb.ManifestAmount_Sats{Sats: sats}}
}

func bpsAmount(bps uint32) *pb.ManifestAmount {
	return &pb.ManifestAmount{Amount: &pb.ManifestAmount_Bps{Bps: bps}}
}

func TestHashTransferManifestIsDeterministic(t *testing.T) {
	first, err := HashTransferManifest(validManifest())
	require.NoError(t, err)
	require.Len(t, first, 32)

	second, err := HashTransferManifest(validManifest())
	require.NoError(t, err)
	require.Equal(t, first, second)
}

func TestHashTransferManifestOrderIndependent(t *testing.T) {
	edgeA := &pb.ManifestEdge{
		SenderIdentityPublicKey:   testPubKey(0x02),
		ReceiverIdentityPublicKey: testPubKey(0x03),
		Amount:                    satsAmount(1000),
	}
	edgeB := &pb.ManifestEdge{
		SenderIdentityPublicKey:   testPubKey(0x02),
		ReceiverIdentityPublicKey: testPubKey(0x04),
		Amount:                    satsAmount(2000),
	}
	feeA := &pb.FeeComponent{
		Source:                    pb.FeeSource_FEE_SOURCE_BASE,
		ReceiverIdentityPublicKey: testPubKey(0x04),
		Amount:                    satsAmount(10),
	}
	feeB := &pb.FeeComponent{
		Source:                    pb.FeeSource_FEE_SOURCE_PARTNER_MARKUP,
		Role:                      pb.FeeRole_FEE_ROLE_PARTNER,
		ReceiverIdentityPublicKey: testPubKey(0x05),
		Amount:                    satsAmount(25),
	}

	sorted := validManifest()
	sorted.Edges = []*pb.ManifestEdge{edgeA, edgeB}
	sorted.Fees = []*pb.FeeComponent{feeB, feeA}

	shuffled := validManifest()
	shuffled.Edges = []*pb.ManifestEdge{proto.Clone(edgeB).(*pb.ManifestEdge), proto.Clone(edgeA).(*pb.ManifestEdge)}
	shuffled.Fees = []*pb.FeeComponent{proto.Clone(feeA).(*pb.FeeComponent), proto.Clone(feeB).(*pb.FeeComponent)}

	sortedHash, err := HashTransferManifest(sorted)
	require.NoError(t, err)
	shuffledHash, err := HashTransferManifest(shuffled)
	require.NoError(t, err)
	require.Equal(t, sortedHash, shuffledHash)
}

func TestHashTransferManifestDoesNotMutateInput(t *testing.T) {
	manifest := validManifest()
	manifest.Edges = append(manifest.Edges, &pb.ManifestEdge{
		SenderIdentityPublicKey:   testPubKey(0x01),
		ReceiverIdentityPublicKey: testPubKey(0x02),
		Amount:                    satsAmount(5),
	})
	manifest.TransferExpiryTime = &timestamppb.Timestamp{Seconds: 1700000000, Nanos: 123456789}
	original := proto.Clone(manifest).(*pb.TransferManifest)

	_, err := HashTransferManifest(manifest)
	require.NoError(t, err)
	require.True(t, proto.Equal(original, manifest), "input manifest was mutated")
}

func TestHashTransferManifestFloorsTimestampsToMillis(t *testing.T) {
	subMillis := validManifest()
	subMillis.TransferExpiryTime = &timestamppb.Timestamp{Seconds: 1700000000, Nanos: 123456789}

	wholeMillis := validManifest()
	wholeMillis.TransferExpiryTime = &timestamppb.Timestamp{Seconds: 1700000000, Nanos: 123000000}

	nextMilli := validManifest()
	nextMilli.TransferExpiryTime = &timestamppb.Timestamp{Seconds: 1700000000, Nanos: 124000000}

	subMillisHash, err := HashTransferManifest(subMillis)
	require.NoError(t, err)
	wholeMillisHash, err := HashTransferManifest(wholeMillis)
	require.NoError(t, err)
	nextMilliHash, err := HashTransferManifest(nextMilli)
	require.NoError(t, err)

	require.Equal(t, wholeMillisHash, subMillisHash)
	require.NotEqual(t, wholeMillisHash, nextMilliHash)
}

func TestHashTransferManifestDistinguishes(t *testing.T) {
	baseHash, err := HashTransferManifest(validManifest())
	require.NoError(t, err)

	for name, mutate := range map[string]func(*pb.TransferManifest){
		"transfer id":       func(m *pb.TransferManifest) { m.TransferId = "01890f5e-45a3-7f2c-8a2b-0242ac120003" },
		"network":           func(m *pb.TransferManifest) { m.Network = pb.Network_MAINNET },
		"edge amount":       func(m *pb.TransferManifest) { m.Edges[0].Amount = satsAmount(1001) },
		"sats vs equal bps": func(m *pb.TransferManifest) { m.Edges[0].Amount = bpsAmount(1000) },
		"transfer expiry":   func(m *pb.TransferManifest) { m.TransferExpiryTime = &timestamppb.Timestamp{Seconds: 1700000000} },
		"added fee": func(m *pb.TransferManifest) {
			m.Fees = []*pb.FeeComponent{{
				Source:                    pb.FeeSource_FEE_SOURCE_BASE,
				ReceiverIdentityPublicKey: testPubKey(0x04),
				Amount:                    satsAmount(10),
			}}
		},
		"swapped sender and receiver": func(m *pb.TransferManifest) {
			edge := m.GetEdges()[0]
			edge.SenderIdentityPublicKey, edge.ReceiverIdentityPublicKey =
				edge.GetReceiverIdentityPublicKey(), edge.GetSenderIdentityPublicKey()
		},
	} {
		t.Run(name, func(t *testing.T) {
			mutated := validManifest()
			mutate(mutated)
			mutatedHash, err := HashTransferManifest(mutated)
			require.NoError(t, err)
			require.NotEqual(t, baseHash, mutatedHash)
		})
	}
}

func TestHashTransferManifestTagSeparation(t *testing.T) {
	manifest := validManifest()

	taggedHash, err := HashTransferManifest(manifest)
	require.NoError(t, err)

	rawObjectHash, err := protohash.Hash(manifest)
	require.NoError(t, err)
	require.NotEqual(t, rawObjectHash, taggedHash)
}

func TestHashTransferManifestValidation(t *testing.T) {
	for name, mutate := range map[string]func(*pb.TransferManifest){
		"zero version":        func(m *pb.TransferManifest) { m.Version = 0 },
		"unsupported version": func(m *pb.TransferManifest) { m.Version = 2 },
		"negative network":    func(m *pb.TransferManifest) { m.Network = pb.Network(-1) },
		"unknown network":     func(m *pb.TransferManifest) { m.Network = pb.Network(999) },
		"negative fee source": func(m *pb.TransferManifest) {
			m.Fees = []*pb.FeeComponent{{Source: pb.FeeSource(-1), Amount: satsAmount(10)}}
		},
		"unknown fee role": func(m *pb.TransferManifest) {
			m.Fees = []*pb.FeeComponent{{Source: pb.FeeSource_FEE_SOURCE_BASE, Role: pb.FeeRole(999), Amount: satsAmount(10)}}
		},
		"sats beyond max supply": func(m *pb.TransferManifest) {
			m.Edges[0].Amount = satsAmount(2_100_000_000_000_001)
		},
		"negative timestamp seconds": func(m *pb.TransferManifest) {
			m.TransferExpiryTime = &timestamppb.Timestamp{Seconds: -1, Nanos: 500000000}
		},
		"timestamp seconds beyond 9999": func(m *pb.TransferManifest) {
			m.QuoteExpiryTime = &timestamppb.Timestamp{Seconds: 253402300800}
		},
		"negative timestamp nanos": func(m *pb.TransferManifest) {
			m.TransferExpiryTime = &timestamppb.Timestamp{Seconds: 1700000000, Nanos: -1}
		},
		"timestamp nanos beyond 1e9": func(m *pb.TransferManifest) {
			m.TransferExpiryTime = &timestamppb.Timestamp{Seconds: 1700000000, Nanos: 1_000_000_000}
		},
		"empty transfer id":   func(m *pb.TransferManifest) { m.TransferId = "" },
		"unspecified network": func(m *pb.TransferManifest) { m.Network = pb.Network_UNSPECIFIED },
		"no edges":            func(m *pb.TransferManifest) { m.Edges = nil },
		"empty sender key":    func(m *pb.TransferManifest) { m.Edges[0].SenderIdentityPublicKey = nil },
		"empty receiver key":  func(m *pb.TransferManifest) { m.Edges[0].ReceiverIdentityPublicKey = nil },
		"missing edge amount": func(m *pb.TransferManifest) { m.Edges[0].Amount = nil },
		"unset edge amount":   func(m *pb.TransferManifest) { m.Edges[0].Amount = &pb.ManifestAmount{} },
		"zero edge amount":    func(m *pb.TransferManifest) { m.Edges[0].Amount = satsAmount(0) },
		"fee unspecified source": func(m *pb.TransferManifest) {
			m.Fees = []*pb.FeeComponent{{Amount: satsAmount(10)}}
		},
		"markup fee without role": func(m *pb.TransferManifest) {
			m.Fees = []*pb.FeeComponent{{Source: pb.FeeSource_FEE_SOURCE_PARTNER_MARKUP, Amount: satsAmount(10)}}
		},
		// Carries a receiver because the receiver check runs first and would mask the amount.
		"zero fee amount": func(m *pb.TransferManifest) {
			m.Fees = []*pb.FeeComponent{{
				Source:                    pb.FeeSource_FEE_SOURCE_BASE,
				ReceiverIdentityPublicKey: testPubKey(0x04),
				Amount:                    satsAmount(0),
			}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			manifest := validManifest()
			mutate(manifest)
			_, err := HashTransferManifest(manifest)
			require.Error(t, err)
		})
	}

	t.Run("nil manifest", func(t *testing.T) {
		_, err := HashTransferManifest(nil)
		require.Error(t, err)
	})

	t.Run("base fee without role is valid", func(t *testing.T) {
		manifest := validManifest()
		manifest.Fees = []*pb.FeeComponent{{
			Source:                    pb.FeeSource_FEE_SOURCE_BASE,
			ReceiverIdentityPublicKey: testPubKey(0x04),
			Amount:                    satsAmount(10),
		}}
		_, err := HashTransferManifest(manifest)
		require.NoError(t, err)
	})

	t.Run("sats at max supply is valid", func(t *testing.T) {
		manifest := validManifest()
		manifest.Edges[0].Amount = satsAmount(2_100_000_000_000_000)
		_, err := HashTransferManifest(manifest)
		require.NoError(t, err)
	})

	t.Run("timestamp at max bound is valid", func(t *testing.T) {
		manifest := validManifest()
		manifest.TransferExpiryTime = &timestamppb.Timestamp{Seconds: 253402300799, Nanos: 999_999_999}
		_, err := HashTransferManifest(manifest)
		require.NoError(t, err)
	})

	for name, receiver := range map[string][]byte{
		"empty":        nil,
		"uncompressed": make([]byte, 32),
		"overlong":     make([]byte, 34),
	} {
		t.Run("fee receiver "+name+" is rejected", func(t *testing.T) {
			manifest := validManifest()
			manifest.Fees = []*pb.FeeComponent{{
				Source:                    pb.FeeSource_FEE_SOURCE_PARTNER_MARKUP,
				Role:                      pb.FeeRole_FEE_ROLE_LS,
				ReceiverIdentityPublicKey: receiver,
				Amount:                    satsAmount(10),
			}}
			_, err := HashTransferManifest(manifest)
			require.ErrorContains(t, err, "fees[0]: receiver_identity_public_key must be 33 bytes")
		})
	}
}
