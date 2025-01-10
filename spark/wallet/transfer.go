package wallet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"math/big"
	"sync"
	"time"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1"
	eciesgo "github.com/ecies/go/v2"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go"
	"github.com/lightsparkdev/spark-go/common"
	secretsharing "github.com/lightsparkdev/spark-go/common/secret_sharing"
	pbcommon "github.com/lightsparkdev/spark-go/proto/common"
	pbfrost "github.com/lightsparkdev/spark-go/proto/frost"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"github.com/lightsparkdev/spark-go/so"
	"github.com/lightsparkdev/spark-go/so/objects"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// LeafKeyTweak is a struct to hold leaf key to tweak.
type LeafKeyTweak struct {
	LeafID            string
	SigningPrivKey    []byte
	NewSigningPrivKey []byte
}

// SendTransfer initiates a transfer from sender.
func SendTransfer(
	ctx context.Context,
	config *Config,
	leaves []LeafKeyTweak,
	receiverIdentityPubkey []byte,
	expiryTime time.Time,
) (*pb.Transfer, error) {
	transferID, err := uuid.NewRandom()
	if err != nil {
		return nil, fmt.Errorf("failed to generate transfer id: %v", err)
	}

	soSendingLeavesMap, err := prepareLeavesTransfer(config, transferID, leaves, receiverIdentityPubkey)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare transfer data: %v", err)
	}

	var transfer *pb.Transfer
	wg := sync.WaitGroup{}
	results := make(chan error, len(config.SigningOperators))
	for identifier, operator := range config.SigningOperators {
		wg.Add(1)
		go func(identifier string, operator *so.SigningOperator) {
			defer wg.Done()
			sparkConn, err := common.NewGRPCConnection(operator.Address)
			if err != nil {
				results <- err
				return
			}
			defer sparkConn.Close()
			sparkClient := pb.NewSparkServiceClient(sparkConn)
			transferResp, err := sparkClient.SendTransfer(ctx, &pb.SendTransferRequest{
				TransferId:                transferID.String(),
				OwnerIdentityPublicKey:    config.IdentityPublicKey(),
				ReceiverIdentityPublicKey: receiverIdentityPubkey,
				ExpiryTime:                timestamppb.New(expiryTime),
				LeavesToSend:              (*soSendingLeavesMap)[identifier],
			})
			if err != nil {
				results <- fmt.Errorf("failed to call SendTransfer: %v", err)
				return
			}
			if transfer == nil {
				transfer = transferResp.Transfer
			} else {
				if !compareTransfers(transfer, transferResp.Transfer) {
					results <- fmt.Errorf("inconsistent transfer response from operators")
					return
				}
			}
		}(identifier, operator)
	}
	wg.Wait()
	close(results)
	for result := range results {
		if result != nil {
			return nil, result
		}
	}
	return transfer, nil
}

func compareTransfers(transfer1, transfer2 *pb.Transfer) bool {
	return transfer1.Id == transfer2.Id &&
		bytes.Equal(transfer1.ReceiverIdentityPublicKey, transfer2.ReceiverIdentityPublicKey) &&
		transfer1.Status == transfer2.Status &&
		transfer1.TotalValue == transfer2.TotalValue &&
		transfer1.ExpiryTime.AsTime().Equal(transfer2.ExpiryTime.AsTime()) &&
		len(transfer1.Leaves) == len(transfer2.Leaves)
}

func prepareLeavesTransfer(config *Config, transferID uuid.UUID, leaves []LeafKeyTweak, receiverIdentityPubkey []byte) (*map[string][]*pb.SendLeafKeyTweak, error) {
	receiverEciesPubKey, err := eciesgo.NewPublicKeyFromBytes(receiverIdentityPubkey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse receiver public key: %v", err)
	}

	leavesTweaksMap := make(map[string][]*pb.SendLeafKeyTweak)
	for _, leaf := range leaves {
		leafTweaksMap, err := prepareSingleLeafTransfer(config, transferID, leaf, receiverEciesPubKey)
		if err != nil {
			return nil, fmt.Errorf("failed to prepare single leaf transfer: %v", err)
		}
		for identifier, leafTweak := range *leafTweaksMap {
			leavesTweaksMap[identifier] = append(leavesTweaksMap[identifier], leafTweak)
		}
	}
	return &leavesTweaksMap, nil
}

func prepareSingleLeafTransfer(config *Config, transferID uuid.UUID, leaf LeafKeyTweak, receiverEciesPubKey *eciesgo.PublicKey) (*map[string]*pb.SendLeafKeyTweak, error) {
	privKeyTweak, err := common.SubtractPrivateKeys(leaf.SigningPrivKey, leaf.NewSigningPrivKey)
	if err != nil {
		return nil, fmt.Errorf("fail to calculate private key tweak: %v", err)

	}

	// Calculate secret tweak shares
	shares, err := secretsharing.SplitSecretWithProofs(
		new(big.Int).SetBytes(privKeyTweak),
		secp256k1.S256().N,
		config.Threshold,
		len(config.SigningOperators),
	)
	if err != nil {
		return nil, fmt.Errorf("fail to split private key tweak: %v", err)
	}

	// Calculate pubkey shares tweak
	pubkeySharesTweak := make(map[string][]byte)
	for identifier, operator := range config.SigningOperators {
		share := findShare(shares, operator.ID)
		if share == nil {
			return nil, fmt.Errorf("failed to find share for operator %d", operator.ID)
		}
		pubkeyTweak := secp256k1.NewPrivateKey(share.Share).PubKey()
		pubkeySharesTweak[identifier] = pubkeyTweak.SerializeCompressed()
	}

	secretCipher, err := eciesgo.Encrypt(receiverEciesPubKey, leaf.NewSigningPrivKey)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt new signing private key: %v", err)
	}

	// Generate signature over Sha256(leaf_id||transfer_id||secret_cipher)
	payload := append(append([]byte(leaf.LeafID), []byte(transferID.String())...), secretCipher...)
	payloadHash := sha256.Sum256(payload)
	signature, err := config.IdentityPrivateKey.Sign(payloadHash[:])
	if err != nil {
		return nil, fmt.Errorf("failed to sign payload: %v", err)

	}

	leafTweaksMap := make(map[string]*pb.SendLeafKeyTweak)
	for identifier, operator := range config.SigningOperators {
		share := findShare(shares, operator.ID)
		if share == nil {
			return nil, fmt.Errorf("failed to find share for operator %d", operator.ID)
		}
		leafTweaksMap[identifier] = &pb.SendLeafKeyTweak{
			LeafId: leaf.LeafID,
			SecretShareTweak: &pb.SecretShareTweak{
				Tweak:  share.Share.Bytes(),
				Proofs: share.Proofs,
			},
			PubkeySharesTweak: pubkeySharesTweak,
			SecretCipher:      secretCipher,
			Signature:         signature.Serialize(),
		}
	}
	return &leafTweaksMap, nil
}

func findShare(shares []*secretsharing.VerifiableSecretShare, operatorID uint64) *secretsharing.VerifiableSecretShare {
	targetShareIndex := big.NewInt(int64(operatorID + 1))
	for _, s := range shares {
		if s.SecretShare.Index.Cmp(targetShareIndex) == 0 {
			return s
		}
	}
	return nil
}

// QueryPendingTransfers queries pending transfers to claim.
func QueryPendingTransfers(
	ctx context.Context,
	config *Config,
) (*pb.QueryPendingTransfersResponse, error) {
	sparkConn, err := common.NewGRPCConnection(config.CoodinatorAddress())
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)
	return sparkClient.QueryPendingTransfers(ctx, &pb.QueryPendingTransfersRequest{
		ReceiverIdentityPublicKey: config.IdentityPublicKey(),
	})
}

// VerifyPendingTransfer verifies signature and decrypt secret cipher for all leaves in the transfer.
func VerifyPendingTransfer(
	ctx context.Context,
	config *Config,
	transfer *pb.Transfer,
) (*map[string][]byte, error) {
	leafPrivKeyMap := make(map[string][]byte)
	senderPubkey, err := secp256k1.ParsePubKey(transfer.SenderIdentityPublicKey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse sender public key: %v", err)
	}

	receiverEciesPrivKey := eciesgo.NewPrivateKeyFromBytes(config.IdentityPrivateKey.Serialize())
	for _, leaf := range transfer.Leaves {
		// Verify signature
		signature, err := secp256k1.ParseSignature(leaf.Signature)
		if err != nil {
			return nil, fmt.Errorf("failed to parse signature: %v", err)
		}
		payload := append(append([]byte(leaf.Leaf.Id), []byte(transfer.Id)...), leaf.SecretCipher...)
		payloadHash := sha256.Sum256(payload)
		if !signature.Verify(payloadHash[:], senderPubkey) {
			return nil, fmt.Errorf("failed to verify signature: %v", err)
		}

		// Decrypt secret cipher
		leafSecret, err := eciesgo.Decrypt(receiverEciesPrivKey, leaf.SecretCipher)
		if err != nil {
			return nil, fmt.Errorf("failed to decrypt secret cipher: %v", err)
		}
		leafPrivKeyMap[leaf.Leaf.Id] = leafSecret

	}
	return &leafPrivKeyMap, nil
}

// ClaimTransfer claims a pending transfer.
func ClaimTransfer(
	ctx context.Context,
	transfer *pb.Transfer,
	config *Config,
	leaves []LeafKeyTweak,
) error {
	err := claimTransferTweakKeys(ctx, transfer, config, leaves)
	if err != nil {
		return fmt.Errorf("failed to tweak keys when claiming leaves: %v", err)
	}

	signatures, err := claimTransferSignRefunds(ctx, transfer, config, leaves)
	if err != nil {
		return fmt.Errorf("failed to sign refunds when claiming leaves: %v", err)
	}

	return finalizeTransfer(ctx, config, signatures)
}

func claimTransferTweakKeys(
	ctx context.Context,
	transfer *pb.Transfer,
	config *Config,
	leaves []LeafKeyTweak,
) error {
	leavesTweaksMap, err := prepareClaimLeavesKeyTweaks(config, leaves)
	if err != nil {
		return fmt.Errorf("failed to prepare transfer data: %v", err)
	}

	wg := sync.WaitGroup{}
	results := make(chan error, len(config.SigningOperators))

	for identifier, operator := range config.SigningOperators {
		wg.Add(1)
		go func(identifier string, operator *so.SigningOperator) {
			defer wg.Done()
			sparkConn, err := common.NewGRPCConnection(operator.Address)
			if err != nil {
				results <- err
				return
			}
			defer sparkConn.Close()
			sparkClient := pb.NewSparkServiceClient(sparkConn)
			_, err = sparkClient.ClaimTransferTweakKeys(ctx, &pb.ClaimTransferTweakKeysRequest{
				TransferId:             transfer.Id,
				OwnerIdentityPublicKey: config.IdentityPublicKey(),
				LeavesToReceive:        (*leavesTweaksMap)[identifier],
			})
			if err != nil {
				results <- fmt.Errorf("failed to call ClaimTransferTweakKeys: %v", err)
			}
		}(identifier, operator)
	}
	wg.Wait()
	close(results)
	for result := range results {
		if result != nil {
			return result
		}
	}
	return nil
}

func prepareClaimLeavesKeyTweaks(config *Config, leaves []LeafKeyTweak) (*map[string][]*pb.ClaimLeafKeyTweak, error) {
	leavesTweaksMap := make(map[string][]*pb.ClaimLeafKeyTweak)
	for _, leaf := range leaves {
		leafTweaksMap, err := prepareClaimLeafKeyTweaks(config, leaf)
		if err != nil {
			return nil, fmt.Errorf("failed to prepare single leaf transfer: %v", err)
		}
		for identifier, leafTweak := range *leafTweaksMap {
			leavesTweaksMap[identifier] = append(leavesTweaksMap[identifier], leafTweak)
		}
	}
	return &leavesTweaksMap, nil
}

func prepareClaimLeafKeyTweaks(config *Config, leaf LeafKeyTweak) (*map[string]*pb.ClaimLeafKeyTweak, error) {
	privKeyTweak, err := common.SubtractPrivateKeys(leaf.SigningPrivKey, leaf.NewSigningPrivKey)
	if err != nil {
		return nil, fmt.Errorf("fail to calculate private key tweak: %v", err)

	}

	// Calculate secret tweak shares
	shares, err := secretsharing.SplitSecretWithProofs(
		new(big.Int).SetBytes(privKeyTweak),
		secp256k1.S256().N,
		config.Threshold,
		len(config.SigningOperators),
	)
	if err != nil {
		return nil, fmt.Errorf("fail to split private key tweak: %v", err)
	}

	// Calculate pubkey shares tweak
	pubkeySharesTweak := make(map[string][]byte)
	for identifier, operator := range config.SigningOperators {
		share := findShare(shares, operator.ID)
		if share == nil {
			return nil, fmt.Errorf("failed to find share for operator %d", operator.ID)
		}
		pubkeyTweak := secp256k1.NewPrivateKey(share.Share).PubKey()
		pubkeySharesTweak[identifier] = pubkeyTweak.SerializeCompressed()
	}

	leafTweaksMap := make(map[string]*pb.ClaimLeafKeyTweak)
	for identifier, operator := range config.SigningOperators {
		share := findShare(shares, operator.ID)
		if share == nil {
			return nil, fmt.Errorf("failed to find share for operator %d", operator.ID)
		}
		leafTweaksMap[identifier] = &pb.ClaimLeafKeyTweak{
			LeafId: leaf.LeafID,
			SecretShareTweak: &pb.SecretShareTweak{
				Tweak:  share.Share.Bytes(),
				Proofs: share.Proofs,
			},
			PubkeySharesTweak: pubkeySharesTweak,
		}
	}
	return &leafTweaksMap, nil
}

type claimLeafData struct {
	SigningPrivKey *secp256k1.PrivateKey
	Tx             *wire.MsgTx
	RefundTx       *wire.MsgTx
	Nonce          *objects.SigningNonce
	Vout           int
}

func claimTransferSignRefunds(
	ctx context.Context,
	transfer *pb.Transfer,
	config *Config,
	leafKeys []LeafKeyTweak,
) ([]*pb.NodeSignatures, error) {
	leafDataMap := make(map[string]*claimLeafData)
	for _, leafKey := range leafKeys {
		privKey, _ := secp256k1.PrivKeyFromBytes(leafKey.NewSigningPrivKey)
		nonce, _ := objects.RandomSigningNonce()
		leafData := &claimLeafData{
			SigningPrivKey: privKey,
			Nonce:          nonce,
		}
		for _, leaf := range transfer.Leaves {
			if leaf.Leaf.Id == leafKey.LeafID {
				tx, _ := common.TxFromRawTxBytes(leaf.Leaf.NodeTx)
				leafData.Tx = tx
				leafData.Vout = int(leaf.Leaf.Vout)
			}
		}
		leafDataMap[leafKey.LeafID] = leafData
	}

	signingJobs, err := prepareClaimTransferOperatorsSigningJobs(transfer, config, leafDataMap)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare signing jobs for claiming transfer: %v", err)
	}
	sparkConn, err := common.NewGRPCConnection(config.CoodinatorAddress())
	if err != nil {
		return nil, err
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)
	response, err := sparkClient.ClaimTransferSignRefunds(ctx, &pb.ClaimTransferSignRefundsRequest{
		TransferId:             transfer.Id,
		OwnerIdentityPublicKey: config.IdentityPublicKey(),
		SigningJobs:            signingJobs,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to call ClaimTransferSignRefunds: %v", err)
	}

	return signRefunds(config, leafDataMap, response.SigningResults)
}

func finalizeTransfer(
	ctx context.Context,
	config *Config,
	signatures []*pb.NodeSignatures,
) error {
	sparkConn, err := common.NewGRPCConnection(config.CoodinatorAddress())
	if err != nil {
		return err
	}
	defer sparkConn.Close()
	sparkClient := pb.NewSparkServiceClient(sparkConn)
	_, err = sparkClient.FinalizeNodeSignatures(ctx, &pb.FinalizeNodeSignaturesRequest{
		Intent:         pbcommon.SignatureIntent_TRANSFER,
		NodeSignatures: signatures,
	})
	return err
}

func signRefunds(
	config *Config,
	leafDataMap map[string]*claimLeafData,
	operatorSigningResults []*pb.ClaimLeafSigningResult,
) ([]*pb.NodeSignatures, error) {
	userSigningJobs := []*pbfrost.FrostSigningJob{}
	jobToAggregateRequestMap := make(map[string]*pbfrost.AggregateFrostRequest)
	jobToLeafMap := make(map[string]string)
	for _, operatorSigningResult := range operatorSigningResults {
		leafData := leafDataMap[operatorSigningResult.LeafId]
		refundTxSighash, _ := common.SigHashFromTx(leafData.RefundTx, 0, leafData.Tx.TxOut[leafData.Vout])
		nonceProto, _ := leafData.Nonce.MarshalProto()
		nonceCommitmentProto, _ := leafData.Nonce.SigningCommitment().MarshalProto()
		userKeyPackage := CreateUserKeyPackage(leafData.SigningPrivKey.Serialize())

		userSigningJobID := uuid.NewString()
		jobToLeafMap[userSigningJobID] = operatorSigningResult.LeafId
		userSigningJobs = append(userSigningJobs, &pbfrost.FrostSigningJob{
			JobId:           userSigningJobID,
			Message:         refundTxSighash,
			KeyPackage:      userKeyPackage,
			VerifyingKey:    operatorSigningResult.VerifyingKey,
			Nonce:           nonceProto,
			Commitments:     operatorSigningResult.RefundTxSigningResult.SigningNonceCommitments,
			UserCommitments: nonceCommitmentProto,
		})

		jobToAggregateRequestMap[userSigningJobID] = &pbfrost.AggregateFrostRequest{
			Message:         refundTxSighash,
			SignatureShares: operatorSigningResult.RefundTxSigningResult.SignatureShares,
			PublicShares:    operatorSigningResult.RefundTxSigningResult.PublicKeys,
			VerifyingKey:    operatorSigningResult.VerifyingKey,
			Commitments:     operatorSigningResult.RefundTxSigningResult.SigningNonceCommitments,
			UserCommitments: nonceCommitmentProto,
			UserPublicKey:   leafData.SigningPrivKey.PubKey().SerializeCompressed(),
		}
	}

	frostConn, _ := common.NewGRPCConnection(config.FrostSignerAddress)
	defer frostConn.Close()
	frostClient := pbfrost.NewFrostServiceClient(frostConn)
	userSignatures, err := frostClient.SignFrost(context.Background(), &pbfrost.SignFrostRequest{
		SigningJobs: userSigningJobs,
		Role:        pbfrost.SigningRole_USER,
	})
	if err != nil {
		return nil, err
	}

	nodeSignatures := []*pb.NodeSignatures{}
	for jobID, userSignature := range userSignatures.Results {
		request := jobToAggregateRequestMap[jobID]
		request.UserSignatureShare = userSignature.SignatureShare
		response, err := frostClient.AggregateFrost(context.Background(), request)
		if err != nil {
			return nil, err
		}
		nodeSignatures = append(nodeSignatures, &pb.NodeSignatures{
			NodeId:            jobToLeafMap[jobID],
			RefundTxSignature: response.Signature,
		})
	}
	return nodeSignatures, nil
}

func prepareClaimTransferOperatorsSigningJobs(
	transfer *pb.Transfer,
	config *Config,
	leafDataMap map[string]*claimLeafData,
) ([]*pb.ClaimLeafSigningJob, error) {
	signingJobs := []*pb.ClaimLeafSigningJob{}
	for _, leaf := range transfer.Leaves {
		leafData := leafDataMap[leaf.Leaf.Id]
		signingPubkey := leafData.SigningPrivKey.PubKey().SerializeCompressed()
		tx, err := common.TxFromRawTxBytes(leaf.Leaf.NodeTx)
		if err != nil {
			return nil, fmt.Errorf("failed to parse raw tx: %v", err)
		}
		// Creat refund tx
		refundTx := wire.NewMsgTx(2)
		// TODO(zhenlu): Handle the case where refund timelock is below 0
		sequence := uint32((1 << 30) | leaf.Leaf.RefundTimelock - spark.TimeLockInterval)
		refundTx.AddTxIn(&wire.TxIn{
			PreviousOutPoint: wire.OutPoint{Hash: tx.TxHash(), Index: 0},
			SignatureScript:  tx.TxOut[0].PkScript,
			Witness:          nil,
			Sequence:         sequence,
		})
		refundP2trAddress, _ := common.P2TRAddressFromPublicKey(signingPubkey, config.Network)
		refundAddress, _ := btcutil.DecodeAddress(*refundP2trAddress, common.NetworkParams(config.Network))
		refundPkScript, _ := txscript.PayToAddrScript(refundAddress)
		refundTx.AddTxOut(wire.NewTxOut(tx.TxOut[0].Value, refundPkScript))
		leafData.RefundTx = refundTx
		var refundBuf bytes.Buffer
		refundTx.Serialize(&refundBuf)
		refundNonceCommitmentProto, _ := leafData.Nonce.SigningCommitment().MarshalProto()

		signingJobs = append(signingJobs, &pb.ClaimLeafSigningJob{
			LeafId: leaf.Leaf.Id,
			RefundTxSigningJob: &pb.SigningJob{
				SigningPublicKey:       signingPubkey,
				RawTx:                  refundBuf.Bytes(),
				SigningNonceCommitment: refundNonceCommitmentProto,
			},
		})
	}
	return signingJobs, nil
}
