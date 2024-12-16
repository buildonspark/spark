package wallet

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"reflect"
	"time"

	"github.com/decred/dcrd/dcrec/secp256k1"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/ecies"
	"github.com/google/uuid"
	"github.com/lightsparkdev/spark-go/common"
	secretsharing "github.com/lightsparkdev/spark-go/common/secret_sharing"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// LeafToTransfer is a struct to hold leaf data to transfer.
type LeafToTransfer struct {
	LeafID            string
	SigningPrivKey    []byte
	NewSigningPrivKey []byte
}

// SendTransfer initiates a transfer from sender.
func SendTransfer(
	ctx context.Context,
	config *Config,
	leaves []LeafToTransfer,
	receiverIdentityPubkey []byte,
	expiryTime time.Time,
) (*pb.Transfer, error) {
	transferID, err := uuid.NewRandom()
	if err != nil {
		return nil, fmt.Errorf("failed to generate transfer id: %v", err)
	}

	requests, err := prepareLeavesTransfer(config, transferID, leaves, receiverIdentityPubkey)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare transfer data: %v", err)
	}

	var transfer *pb.Transfer
	for identifier, operator := range config.SigningOperators {
		sparkConn, err := common.NewGRPCConnection(operator.Address)
		if err != nil {
			return nil, err
		}
		defer sparkConn.Close()
		sparkClient := pb.NewSparkServiceClient(sparkConn)
		transferResp, err := sparkClient.SendTransfer(ctx, &pb.SendTransferRequest{
			TransferId:                transferID.String(),
			OwnerIdentityPublicKey:    config.IdentityPublicKey(),
			ReceiverIdentityPublicKey: receiverIdentityPubkey,
			ExpiryTime:                timestamppb.New(expiryTime),
			Tranfers:                  (*requests)[identifier],
		})
		if err != nil {
			return nil, fmt.Errorf("failed to call SendTransfer: %v", err)
		}
		if transfer == nil {
			transfer = transferResp.Transfer
		} else {
			if !compareTransfers(transfer, transferResp.Transfer) {
				return nil, fmt.Errorf("inconsistent transfer response from operators")
			}
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
		reflect.DeepEqual(transfer1.LeafIds, transfer1.LeafIds)
}

func prepareLeavesTransfer(config *Config, transferID uuid.UUID, leaves []LeafToTransfer, receiverIdentityPubkey []byte) (*map[string][]*pb.LeafTransferRequest, error) {
	receiverSecpPubkey, err := secp256k1.ParsePubKey(receiverIdentityPubkey)
	if err != nil {
		return nil, fmt.Errorf("failed to parse receiver public key: %v", err)
	}
	receiverEcdsaPubkey, _ := crypto.UnmarshalPubkey(receiverSecpPubkey.SerializeUncompressed())
	receiverEciesPubKey := ecies.ImportECDSAPublic(receiverEcdsaPubkey)

	transferRequests := make(map[string][]*pb.LeafTransferRequest)
	for _, leaf := range leaves {
		requests, err := prepareSingleLeafTransfer(config, transferID, leaf, receiverEciesPubKey)
		if err != nil {
			return nil, fmt.Errorf("failed to prepare single leaf transfer: %v", err)
		}
		for identifier, request := range *requests {
			transferRequests[identifier] = append(transferRequests[identifier], request)
		}
	}
	return &transferRequests, nil
}

func prepareSingleLeafTransfer(config *Config, transferID uuid.UUID, leaf LeafToTransfer, receiverEciesPubKey *ecies.PublicKey) (*map[string]*pb.LeafTransferRequest, error) {
	privKeyTweak, err := common.SubtractPrivateKeys(leaf.NewSigningPrivKey, leaf.SigningPrivKey)
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

	secretCipher, err := ecies.Encrypt(rand.Reader, receiverEciesPubKey, leaf.NewSigningPrivKey, nil, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to encrypt new signing private key: %v", err)
	}

	// Generate signature over Sha256(leaf_id||transfer_id||secret_cipher)
	payload := append(append([]byte(leaf.LeafID), transferID[:]...), secretCipher...)
	signature, err := config.IdentityPrivateKey.Sign(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to sign payload: %v", err)

	}

	transferRequests := make(map[string]*pb.LeafTransferRequest)
	for identifier, operator := range config.SigningOperators {
		share := findShare(shares, operator.ID)
		if share == nil {
			return nil, fmt.Errorf("failed to find share for operator %d", operator.ID)
		}
		transferRequests[identifier] = &pb.LeafTransferRequest{
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
	return &transferRequests, nil
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
