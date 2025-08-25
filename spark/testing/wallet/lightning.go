package wallet

import (
	"context"
	"fmt"
	"time"

	"github.com/lightsparkdev/spark/common/keys"

	"github.com/google/uuid"

	pbfrost "github.com/lightsparkdev/spark/proto/frost"
	pb "github.com/lightsparkdev/spark/proto/spark"
	decodepay "github.com/nbd-wtf/ln-decodepay"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// SwapNodesForPreimage swaps a node for a preimage of a Lightning invoice.
func SwapNodesForPreimage(
	ctx context.Context,
	config *TestWalletConfig,
	leaves []LeafKeyTweak,
	receiverIdentityPubKey keys.Public,
	paymentHash []byte,
	invoiceString *string,
	feeSats uint64,
	isInboundPayment bool,
	amountSats uint64,
) (*pb.InitiatePreimageSwapResponse, error) {
	// SSP asks for signing commitment
	conn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to coordinator: %w", err)
	}
	defer conn.Close()

	token, err := AuthenticateWithConnection(ctx, config, conn)
	if err != nil {
		return nil, fmt.Errorf("failed to authenticate with server: %w", err)
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
	signerConn, err := config.NewFrostGRPCConnection()
	if err != nil {
		return nil, fmt.Errorf("failed to connect to frost signer: %w", err)
	}
	defer signerConn.Close()

	signingJobs, refundTxs, userCommitments, err := prepareFrostSigningJobsForUserSignedRefund(leaves, signingCommitments.SigningCommitments, receiverIdentityPubKey)
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
		return nil, fmt.Errorf("failed to generate transfer id: %w", err)
	}
	bolt11String := ""
	if invoiceString != nil {
		bolt11String = *invoiceString
		bolt11, err := decodepay.Decodepay(bolt11String)
		if err != nil {
			return nil, fmt.Errorf("unable to decode invoice: %w", err)
		}
		if bolt11.MSatoshi > 0 {
			amountSats = uint64(bolt11.MSatoshi / 1000)
		}
	}
	reason := pb.InitiatePreimageSwapRequest_REASON_SEND
	if isInboundPayment {
		reason = pb.InitiatePreimageSwapRequest_REASON_RECEIVE
	}
	response, err := client.InitiatePreimageSwapV2(tmpCtx, &pb.InitiatePreimageSwapRequest{
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
			OwnerIdentityPublicKey:    config.IdentityPublicKey().Serialize(),
			ReceiverIdentityPublicKey: receiverIdentityPubKey.Serialize(),
			LeavesToSend:              leafSigningJobs,
			ExpiryTime:                timestamppb.New(time.Now().Add(2 * time.Minute)),
		},
		ReceiverIdentityPublicKey: receiverIdentityPubKey.Serialize(),
		FeeSats:                   feeSats,
	})
	if err != nil {
		return nil, err
	}
	return response, nil
}

func ReturnLightningPayment(
	ctx context.Context,
	config *TestWalletConfig,
	paymentHash []byte,
) error {
	conn, err := config.NewCoordinatorGRPCConnection()
	if err != nil {
		return fmt.Errorf("failed to connect to coordinator: %w", err)
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
		UserIdentityPublicKey: config.IdentityPublicKey().Serialize(),
	})
	if err != nil {
		return err
	}
	return nil
}
