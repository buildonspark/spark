package wallet

import (
	"context"
	"fmt"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1/v4"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark/common"
	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pb "github.com/lightsparkdev/spark/proto/spark"
	decodepay "github.com/nbd-wtf/ln-decodepay"
	"google.golang.org/protobuf/types/known/timestamppb"
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
	signingJobs, refundTxs, userCommitments, err := prepareFrostSigningJobsForUserSignedRefund(leaves, signingCommitments.SigningCommitments, receiverIdentityPubkey)
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
			ExpiryTime:                timestamppb.New(time.Now().Add(2 * time.Minute)),
		},
		ReceiverIdentityPublicKey: receiverIdentityPubkeyBytes,
		FeeSats:                   feeSats,
	})
	if err != nil {
		return nil, err
	}
	return response, nil
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
