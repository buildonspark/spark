package wallet

import (
	"context"
	"fmt"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4/ecdsa"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	"github.com/lightsparkdev/spark/common/keys"
	pb "github.com/lightsparkdev/spark/proto/spark"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// PreimageSwapV4 describes a preimage swap riding StartTransferV3Request: every leaf names its
// own receiver, and a manifest binds the whole routing to the senders that signed it.
type PreimageSwapV4 struct {
	Leaves        []LeafKeyTweak
	LeafReceivers map[string]keys.Public
	// The counterparty signs the manifest hash as well as keying the preimage request, so the
	// driver needs its private half.
	CounterpartyPrivKey keys.Private
	PaymentHash         []byte
	Invoice             string
	AmountSats          uint64
	Reason              pb.InitiatePreimageSwapRequest_Reason
	// A zero ExpiryTime omits the expiry from both request and manifest, which is the only
	// shape a non-HODL receive can take: the SO strips the request's expiry before the fanout.
	ExpiryTime time.Time
	// Edges replaces the exact cover the driver derives, for callers that must present a
	// manifest the operators are expected to refuse.
	Edges []*pb.ManifestEdge
}

// ManifestEdgesForLeaves derives the exact cover the operators require: one edge per
// (sender, receiver) pair, totalling the value of every leaf routed to that receiver.
func ManifestEdgesForLeaves(sender keys.Public, leaves []LeafKeyTweak, leafReceivers map[string]keys.Public) ([]*pb.ManifestEdge, error) {
	edges := make([]*pb.ManifestEdge, 0, len(leaves))
	edgeIndexByReceiver := make(map[keys.Public]int, len(leaves))

	for _, leaf := range leaves {
		leafID := leaf.Leaf.GetId()
		receiver, ok := leafReceivers[leafID]
		if !ok {
			return nil, fmt.Errorf("no receiver for leaf %s", leafID)
		}
		index, seen := edgeIndexByReceiver[receiver]
		if !seen {
			edges = append(edges, &pb.ManifestEdge{
				SenderIdentityPublicKey:   sender.Serialize(),
				ReceiverIdentityPublicKey: receiver.Serialize(),
			})
			index = len(edges) - 1
			edgeIndexByReceiver[receiver] = index
		}
		sats := edges[index].GetAmount().GetSats() + leaf.Leaf.GetValue()
		edges[index].Amount = &pb.ManifestAmount{Amount: &pb.ManifestAmount_Sats{Sats: sats}}
	}

	return edges, nil
}

// SignTransferManifest produces the manifest-hash signature every contributing sender and the
// swap counterparty must each supply.
func SignTransferManifest(signer keys.Private, manifest *pb.TransferManifest) ([]byte, error) {
	manifestHash, err := common.HashTransferManifest(manifest)
	if err != nil {
		return nil, fmt.Errorf("unable to hash transfer manifest: %w", err)
	}
	return ecdsa.Sign(signer.ToBTCEC(), manifestHash).Serialize(), nil
}

// BuildInitiatePreimageSwapV4Request assembles the per-leaf routed transfer package, the manifest
// covering it, and both manifest signatures.
func BuildInitiatePreimageSwapV4Request(
	ctx context.Context,
	config *TestWalletConfig,
	client pb.SparkServiceClient,
	transferID uuid.UUID,
	swap PreimageSwapV4,
) (*pb.InitiatePreimageSwapV4Request, error) {
	transferPackage, err := CreateTransferPackageV3(ctx, transferID, config, client, swap.Leaves, swap.LeafReceivers)
	if err != nil {
		return nil, fmt.Errorf("unable to create v3 transfer package: %w", err)
	}

	edges := swap.Edges
	if edges == nil {
		edges, err = ManifestEdgesForLeaves(config.IdentityPublicKey(), swap.Leaves, swap.LeafReceivers)
		if err != nil {
			return nil, err
		}
	}

	manifest := &pb.TransferManifest{
		Version:    common.SupportedTransferManifestVersion,
		TransferId: transferID.String(),
		Network:    config.ProtoNetwork(),
		Edges:      edges,
	}
	var expiryTime *timestamppb.Timestamp
	if !swap.ExpiryTime.IsZero() {
		expiryTime = timestamppb.New(swap.ExpiryTime)
		manifest.TransferExpiryTime = expiryTime
	}

	senderManifestSignature, err := SignTransferManifest(config.IdentityPrivateKey, manifest)
	if err != nil {
		return nil, err
	}
	counterpartyManifestSignature, err := SignTransferManifest(swap.CounterpartyPrivKey, manifest)
	if err != nil {
		return nil, err
	}

	receiverPubKeys := make(map[string][]byte, len(swap.LeafReceivers))
	for leafID, receiver := range swap.LeafReceivers {
		receiverPubKeys[leafID] = receiver.Serialize()
	}

	return &pb.InitiatePreimageSwapV4Request{
		PaymentHash: swap.PaymentHash,
		InvoiceAmount: &pb.InvoiceAmount{
			ValueSats:          swap.AmountSats,
			InvoiceAmountProof: &pb.InvoiceAmountProof{Bolt11Invoice: swap.Invoice},
		},
		Reason:                        swap.Reason,
		CounterpartyIdentityPublicKey: swap.CounterpartyPrivKey.Public().Serialize(),
		CounterpartyManifestSignature: counterpartyManifestSignature,
		TransferV3Request: &pb.StartTransferV3Request{
			TransferId: transferID.String(),
			SenderPackages: []*pb.SenderTransferPackage{{
				OwnerIdentityPublicKey:     config.IdentityPublicKey().Serialize(),
				TransferPackage:            transferPackage,
				ReceiverIdentityPublicKeys: receiverPubKeys,
				ManifestHashSignature:      senderManifestSignature,
			}},
			ExpiryTime:       expiryTime,
			TransferManifest: manifest,
		},
	}, nil
}

// InitiatePreimageSwapV4 drives initiate_preimage_swap_v4 as the sender config names, returning
// the transfer id it chose so callers can assert on state the response may not carry.
func InitiatePreimageSwapV4(
	ctx context.Context,
	config *TestWalletConfig,
	swap PreimageSwapV4,
) (*pb.InitiatePreimageSwapResponse, uuid.UUID, error) {
	transferID, err := uuid.NewV7()
	if err != nil {
		return nil, uuid.Nil, fmt.Errorf("failed to generate transfer id: %w", err)
	}

	conn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, transferID, fmt.Errorf("failed to connect to coordinator: %w", err)
	}
	defer conn.Close()

	token, err := AuthenticateWithConnection(ctx, config, conn)
	if err != nil {
		return nil, transferID, fmt.Errorf("failed to authenticate with server: %w", err)
	}
	authCtx := ContextWithToken(ctx, token)

	client := pb.NewSparkServiceClient(conn)
	req, err := BuildInitiatePreimageSwapV4Request(authCtx, config, client, transferID, swap)
	if err != nil {
		return nil, transferID, err
	}

	response, err := client.InitiatePreimageSwapV4(authCtx, req)
	if err != nil {
		return nil, transferID, err
	}
	return response, transferID, nil
}
