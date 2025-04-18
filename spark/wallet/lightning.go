package wallet

import (
	"bytes"
	"context"
	"fmt"

	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go"
	"github.com/lightsparkdev/spark-go/common"
	pbcommon "github.com/lightsparkdev/spark-go/proto/common"
	pbfrost "github.com/lightsparkdev/spark-go/proto/frost"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so/objects"
	decodepay "github.com/nbd-wtf/ln-decodepay"
)

// SwapNodesForLightning swaps a node for a preimage of a Lightning invoice.
func SwapNodesForPreimage(
	ctx context.Context,
	config *Config,
	leaves []LeafKeyTweak,
	receiverIdentityPubkeyBytes []byte,
	paymentHash []byte,
	invoiceString *string,
	feeSats uint64,
	isInboundPayment bool,
) (*pb.InitiatePreimageSwapResponse, error) {
	// SSP asks for signing commitment
	conn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress(), nil)
	if err != nil {
		return nil, err
	}
	defer conn.Close()

	token, err := AuthenticateWithConnection(ctx, config, conn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate with server: %v", err)
	}
	tmpCtx := ContextWithToken(ctx, token)

	client := pb.NewSparkServiceClient(conn)
	nodeIDs := make([]string, len(leaves))
	for i, leaf := range leaves {
		nodeIDs[i] = leaf.Leaf.Id
	}
	signingCommitments, err := client.GetSigningCommitments(tmpCtx, &pb.GetSigningCommitmentsRequest{
		NodeIds: nodeIDs,
	})
	if err != nil {
		return nil, err
	}

	// SSP signs partial refund tx to receiver
	signerConn, err := common.NewGRPCConnectionWithoutTLS(config.FrostSignerAddress, nil)
	if err != nil {
		return nil, err
	}
	defer signerConn.Close()

	receiverIdentityPubkey, err := secp256k1.ParsePubKey(receiverIdentityPubkeyBytes)
	if err != nil {
		return nil, err
	}
	signingJobs, refundTxs, userCommitments, err := prepareFrostSigningJobs(leaves, signingCommitments.SigningCommitments, receiverIdentityPubkey)
	if err != nil {
		return nil, err
	}

	signerClient := pbfrost.NewFrostServiceClient(signerConn)
	signingResults, err := signerClient.SignFrost(ctx, &pbfrost.SignFrostRequest{
		SigningJobs: signingJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		return nil, err
	}

	leafSigningJobs, err := prepareLeafSigningJobs(
		leaves,
		refundTxs,
		signingResults.Results,
		userCommitments,
		signingCommitments.SigningCommitments,
	)
	if err != nil {
		return nil, err
	}

	// SSP calls SO to get the preimage
	transferID, err := uuid.NewV7()
	if err != nil {
		return nil, fmt.Errorf("failed to generate transfer id: %v", err)
	}
	bolt11String := ""
	var amountSats uint64
	if invoiceString != nil {
		bolt11String = *invoiceString
		bolt11, err := decodepay.Decodepay(bolt11String)
		if err != nil {
			return nil, fmt.Errorf("unable to decode invoice: %v", err)
		}
		amountSats = uint64(bolt11.MSatoshi / 1000)
	}
	reason := pb.InitiatePreimageSwapRequest_REASON_SEND
	if isInboundPayment {
		reason = pb.InitiatePreimageSwapRequest_REASON_RECEIVE
	}
	response, err := client.InitiatePreimageSwap(tmpCtx, &pb.InitiatePreimageSwapRequest{
		PaymentHash: paymentHash,
		Reason:      reason,
		InvoiceAmount: &pb.InvoiceAmount{
			InvoiceAmountProof: &pb.InvoiceAmountProof{
				Bolt11Invoice: bolt11String,
			},
			ValueSats: amountSats,
		},
		Transfer: &pb.StartUserSignedTransferRequest{
			TransferId:                transferID.String(),
			OwnerIdentityPublicKey:    config.IdentityPublicKey(),
			ReceiverIdentityPublicKey: receiverIdentityPubkeyBytes,
			LeavesToSend:              leafSigningJobs,
		},
		ReceiverIdentityPublicKey: receiverIdentityPubkeyBytes,
		FeeSats:                   feeSats,
	})
	if err != nil {
		return nil, err
	}
	return response, nil
}

func prepareFrostSigningJobs(
	leaves []LeafKeyTweak,
	signingCommitments []*pb.RequestedSigningCommitments,
	receiverIdentityPubkey *secp256k1.PublicKey,
) ([]*pbfrost.FrostSigningJob, [][]byte, []*objects.SigningCommitment, error) {
	signingJobs := []*pbfrost.FrostSigningJob{}
	refundTxs := make([][]byte, len(leaves))
	userCommitments := make([]*objects.SigningCommitment, len(leaves))
	for i, leaf := range leaves {
		nodeTx, err := common.TxFromRawTxBytes(leaf.Leaf.NodeTx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to parse node tx: %v", err)
		}
		nodeOutPoint := wire.OutPoint{Hash: nodeTx.TxHash(), Index: 0}
		currRefundTx, err := common.TxFromRawTxBytes(leaf.Leaf.RefundTx)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to parse refund tx: %v", err)
		}
		nextSequence, err := spark.NextSequence(currRefundTx.TxIn[0].Sequence)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to get next sequence: %v", err)
		}
		amountSats := nodeTx.TxOut[0].Value
		refundTx, err := createRefundTx(nextSequence, &nodeOutPoint, amountSats, receiverIdentityPubkey)
		if err != nil {
			return nil, nil, nil, err
		}
		var refundBuf bytes.Buffer
		err = refundTx.Serialize(&refundBuf)
		if err != nil {
			return nil, nil, nil, err
		}
		refundTxs[i] = refundBuf.Bytes()

		sighash, err := common.SigHashFromTx(refundTx, 0, nodeTx.TxOut[0])
		if err != nil {
			return nil, nil, nil, fmt.Errorf("failed to calculate sighash: %v", err)
		}

		signingNonce, err := objects.RandomSigningNonce()
		if err != nil {
			return nil, nil, nil, err
		}
		signingNonceProto, err := signingNonce.MarshalProto()
		if err != nil {
			return nil, nil, nil, err
		}
		userCommitmentProto, err := signingNonce.SigningCommitment().MarshalProto()
		if err != nil {
			return nil, nil, nil, err
		}
		userCommitments[i] = signingNonce.SigningCommitment()

		userKeyPackage := CreateUserKeyPackage(leaf.SigningPrivKey)

		signingJobs = append(signingJobs, &pbfrost.FrostSigningJob{
			JobId:           leaf.Leaf.Id,
			Message:         sighash,
			KeyPackage:      userKeyPackage,
			VerifyingKey:    leaf.Leaf.VerifyingPublicKey,
			Nonce:           signingNonceProto,
			Commitments:     signingCommitments[i].SigningNonceCommitments,
			UserCommitments: userCommitmentProto,
		})
	}
	return signingJobs, refundTxs, userCommitments, nil
}

func prepareLeafSigningJobs(
	leaves []LeafKeyTweak,
	refundTxs [][]byte,
	signingResults map[string]*pbcommon.SigningResult,
	userCommitments []*objects.SigningCommitment,
	signingCommitments []*pb.RequestedSigningCommitments,
) ([]*pb.UserSignedTxSigningJob, error) {
	leafSigningJobs := []*pb.UserSignedTxSigningJob{}
	for i, leaf := range leaves {
		userCommitmentProto, err := userCommitments[i].MarshalProto()
		if err != nil {
			return nil, err
		}
		leafSigningJobs = append(leafSigningJobs, &pb.UserSignedTxSigningJob{
			LeafId:                 leaf.Leaf.Id,
			SigningPublicKey:       leaf.SigningPrivKey,
			RawTx:                  refundTxs[i],
			SigningNonceCommitment: userCommitmentProto,
			UserSignature:          signingResults[leaf.Leaf.Id].SignatureShare,
			SigningCommitments: &pb.SigningCommitments{
				SigningCommitments: signingCommitments[i].SigningNonceCommitments,
			},
		})
	}
	return leafSigningJobs, nil
}

func ReturnLightningPayment(
	ctx context.Context,
	config *Config,
	paymentHash []byte,
) error {
	conn, err := common.NewGRPCConnectionWithTestTLS(config.CoodinatorAddress(), nil)
	if err != nil {
		return err
	}
	defer conn.Close()

	token, err := AuthenticateWithConnection(ctx, config, conn)
	if err != nil {
		return err
	}
	tmpCtx := ContextWithToken(ctx, token)

	client := pb.NewSparkServiceClient(conn)
	_, err = client.ReturnLightningPayment(tmpCtx, &pb.ReturnLightningPaymentRequest{
		PaymentHash:           paymentHash,
		UserIdentityPublicKey: config.IdentityPublicKey(),
	})
	if err != nil {
		return err
	}
	return nil
}
