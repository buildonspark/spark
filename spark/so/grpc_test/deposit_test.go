package grpctest

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"log"
	"testing"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/chaincfg/chainhash"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1"
	"github.com/lightsparkdev/spark-go/common"
	pbcommon "github.com/lightsparkdev/spark-go/proto/common"
	pbmock "github.com/lightsparkdev/spark-go/proto/mock"
	pb "github.com/lightsparkdev/spark-go/proto/spark"
)

func TestGenerateDepositAddress(t *testing.T) {
	conn, err := common.NewGRPCConnection("localhost:8535")
	if err != nil {
		t.Fatalf("failed to connect to operator: %v", err)
	}
	defer conn.Close()

	client := pb.NewSparkServiceClient(conn)

	pubkey, err := hex.DecodeString("0330d50fd2e26d274e15f3dcea34a8bb611a9d0f14d1a9b1211f3608b3b7cd56c7")
	if err != nil {
		t.Fatalf("failed to decode public key: %v", err)
	}

	resp, err := client.GenerateDepositAddress(context.Background(), &pb.GenerateDepositAddressRequest{
		SigningPublicKey:  pubkey,
		IdentityPublicKey: pubkey,
	})
	if err != nil {
		t.Fatalf("failed to generate deposit address: %v", err)
	}

	if resp.Address == "" {
		t.Fatalf("deposit address is empty")
	}
}

func createTestP2TRTransaction(p2trAddress string, amountSats int64) (*wire.MsgTx, error) {
	// Create new transaction
	tx := wire.NewMsgTx(wire.TxVersion)

	// Add a dummy input
	prevOut := wire.NewOutPoint(&chainhash.Hash{}, 0) // Empty hash and index 0
	txIn := wire.NewTxIn(prevOut, nil, [][]byte{})

	// For taproot, we need some form of witness data
	// This is just dummy data for testing
	txIn.Witness = wire.TxWitness{
		[]byte{}, // Empty witness element as placeholder
	}
	tx.AddTxIn(txIn)

	// Decode the P2TR address
	addr, err := btcutil.DecodeAddress(p2trAddress, &chaincfg.MainNetParams)
	if err != nil {
		return nil, fmt.Errorf("error decoding address: %v", err)
	}

	// Create P2TR output script
	pkScript, err := txscript.PayToAddrScript(addr)
	if err != nil {
		return nil, fmt.Errorf("error creating output script: %v", err)
	}

	// Create the output
	txOut := wire.NewTxOut(amountSats, pkScript)
	tx.AddTxOut(txOut)

	return tx, nil
}

func TestStartTreeCreation(t *testing.T) {
	conn, err := common.NewGRPCConnection("localhost:8535")
	if err != nil {
		t.Fatalf("failed to connect to operator: %v", err)
	}
	defer conn.Close()

	client := pb.NewSparkServiceClient(conn)
	mockClient := pbmock.NewMockServiceClient(conn)

	userPubkey, _ := hex.DecodeString("0330d50fd2e26d274e15f3dcea34a8bb611a9d0f14d1a9b1211f3608b3b7cd56c7")

	depositResp, err := client.GenerateDepositAddress(context.Background(), &pb.GenerateDepositAddressRequest{
		SigningPublicKey:  userPubkey,
		IdentityPublicKey: userPubkey,
	})
	if err != nil {
		t.Fatalf("failed to generate deposit address: %v", err)
	}

	// Creat deposit tx
	depositTx, err := createTestP2TRTransaction(depositResp.Address, 100_000)
	if err != nil {
		t.Fatalf("failed to create deposit tx: %v", err)
	}
	vout := 0
	var buf bytes.Buffer
	err = depositTx.Serialize(&buf)
	if err != nil {
		t.Fatalf("failed to serialize deposit tx: %v", err)
	}
	depositTxHex := hex.EncodeToString(buf.Bytes())
	decodedBytes, err := hex.DecodeString(depositTxHex)
	if err != nil {
		t.Fatalf("failed to decode deposit tx hex: %v", err)
	}
	depositTx, err = common.TxFromRawTxBytes(decodedBytes)
	if err != nil {
		t.Fatalf("failed to deserilize deposit tx: %v", err)
	}

	log.Printf("deposit tx: %s", depositTxHex)
	mockClient.SetMockOnchainTx(context.Background(), &pbmock.SetMockOnchainTxRequest{
		Txid: depositTx.TxID(),
		Tx:   depositTxHex,
	})

	// Creat root tx
	rootTx := wire.NewMsgTx(2)
	rootTx.AddTxIn(wire.NewTxIn(
		&wire.OutPoint{Hash: depositTx.TxHash(), Index: uint32(vout)},
		depositTx.TxOut[0].PkScript,
		nil, // witness
	))
	rootTx.AddTxOut(wire.NewTxOut(100_000, depositTx.TxOut[0].PkScript))
	var rootBuf bytes.Buffer
	rootTx.Serialize(&rootBuf)
	rootNonceHidingPriv, _ := secp256k1.GeneratePrivateKey()
	rootNonceBidingPriv, _ := secp256k1.GeneratePrivateKey()
	rootNonceCommitment := pbcommon.SigningCommitment{
		Hiding:  rootNonceHidingPriv.PubKey().SerializeCompressed(),
		Binding: rootNonceBidingPriv.PubKey().SerializeCompressed(),
	}

	// Creat refund tx
	refundTx := wire.NewMsgTx(2)
	refundTx.AddTxIn(wire.NewTxIn(
		&wire.OutPoint{Hash: rootTx.TxHash(), Index: 0},
		rootTx.TxOut[0].PkScript,
		nil, // witness
	))
	refundP2trAddress, _ := common.P2TRAddressFromPublicKey(userPubkey, common.Regtest)
	refundAddress, _ := btcutil.DecodeAddress(*refundP2trAddress, common.NetworkParams(common.Regtest))
	refundPkScript, _ := txscript.PayToAddrScript(refundAddress)
	refundTx.AddTxOut(wire.NewTxOut(100_000, refundPkScript))
	refundTx.LockTime = 60000
	var refundBuf bytes.Buffer
	refundTx.Serialize(&refundBuf)
	refundNonceHidingPriv, _ := secp256k1.GeneratePrivateKey()
	refundNonceBidingPriv, _ := secp256k1.GeneratePrivateKey()
	refundNonceCommitment := pbcommon.SigningCommitment{
		Hiding:  refundNonceHidingPriv.PubKey().SerializeCompressed(),
		Binding: refundNonceBidingPriv.PubKey().SerializeCompressed(),
	}

	treeResponse, err := client.StartTreeCreation(context.Background(), &pb.StartTreeCreationRequest{
		IdentityPublicKey: userPubkey,
		OnChainUtxo: &pb.UTXO{
			Txid: depositTx.TxID(),
			Vout: uint32(vout),
		},
		RootTxSigningJob: &pb.SigningJob{
			RawTx:                  rootBuf.Bytes(),
			SigningPublicKey:       userPubkey,
			SigningNonceCommitment: &rootNonceCommitment,
		},
		RefundTxSigningJob: &pb.SigningJob{
			RawTx:                  refundBuf.Bytes(),
			SigningPublicKey:       userPubkey,
			SigningNonceCommitment: &refundNonceCommitment,
		},
	})
	if err != nil {
		t.Fatalf("failed to start tree creation with error: %v", err)
	}
	if treeResponse.TreeId == "" {
		t.Fatalf("failed to start tree creation")
	}
}
