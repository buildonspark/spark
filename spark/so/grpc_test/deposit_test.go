package grpctest

import (
	"bytes"
	"context"
	"encoding/hex"
	"testing"

	"github.com/btcsuite/btcd/btcutil"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/decred/dcrd/dcrec/secp256k1"
	"github.com/lightsparkdev/spark-go/common"
	pbcommon "github.com/lightsparkdev/spark-go/proto/common"
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

func TestStartTreeCreation(t *testing.T) {
	conn, err := common.NewGRPCConnection("localhost:8535")
	if err != nil {
		t.Fatalf("failed to connect to operator: %v", err)
	}
	defer conn.Close()

	client := pb.NewSparkServiceClient(conn)

	userPubkey, _ := hex.DecodeString("0330d50fd2e26d274e15f3dcea34a8bb611a9d0f14d1a9b1211f3608b3b7cd56c7")

	// Creat deposit tx
	depositTx, _ := common.TxFromTxHex("0200000000010811b3595f26abbeb3f2d80a0e14c2cb0e7f18ce01530740bd4f5daccdb47a1a230000000000fdffffff1dc0aec0699ab25b78774a0d23133c15e80753b5a452ebeabd0bd7d1a6f8cd4f0000000000fdffffffcb60fc393127de3e3990abb041fd572d5122ed38d025273145dfe68f75c4cdd90000000000fdfffffffe8ac07733e1b4ac9a4b0f7be6651d19293202473874ea736b6373db087091c40000000000fdffffff13a7592bf82f81ee8225fb4424c5805b86510be95d38f69b8d5246aecbc852930000000000fdffffff36a4ef7452399e1115f746e34bdeb53c04acba6e773b3a8049ab6c6a1b0342380000000000fdffffff574c840dbb10f4c3f64f95ecbb56653765efd4bff6d28ce3414aa0f446d0dda50000000000fdffffff8328f996704dd4bc01e0ea2c3b92330517886cfd172b7d4ace8f6df2032bb3260000000000fdffffff02f6ff4cfe15000000225120b3bb3fa338472a34467c68e92a4d04007d1bd74132be6e43a8a8fc650dde9946a086010000000000225120f17edec0bd73bb8412b167b92d5550096b6eed5a0f4e2fd528d3d958e0f0a0a302473044022053d6464a299bca45b16af750b7b2ec3750ecd86dac48acb5d0f656b058a7ed1002207e10edbf812162735bcb81998cb76d2eb556f0b280c19cd4a0a6dd93db06ca830121036735f72d6aee497b862bf9d14c92dac211493cdf86013d2023426dc18fa3f2920247304402201caa33d3f6929621979b6636dea12df5a421e9e824528e68983a50b3bfa12a5d02206c173e131045e50a2cbda5f131b201a8fab4df33b96805b28dd7c27e6cb6ac8301210383f8291eeb13ad6d9043025cb13ed4d092e45cb99ad7116f1fa3de1c747a832d02473044022053ec14ddbcc3776606c2b76522851c80011db36b1741c7e2fb211faa7c2b3b4002204b3ddc4a9a31557fa1ac94736576e1f83a21e624ced1f236dfaffac2cd0d919f0121036735f72d6aee497b862bf9d14c92dac211493cdf86013d2023426dc18fa3f29202473044022053c4364bd8e1d8bc7a37f550adf33dd5967999652dbe35e02f0218fed2e2484b022049e5f184464c86c5e0a64877ce8dc7b4abb5574e95ff59eca61a10c1578b6ce80121036735f72d6aee497b862bf9d14c92dac211493cdf86013d2023426dc18fa3f29202473044022057e596b8c4f3d8b6eba0bdf8faf6d7c4774dcb2f11d8a69dc914866590143d4a022049a48bc91ebf2025407c38a6b74b6c9244abb5395c3400123221e1f9aba2f0a00121036735f72d6aee497b862bf9d14c92dac211493cdf86013d2023426dc18fa3f2920247304402203e991e09aa59eda61f7ed3536b4155b723b758ffea4e0f3e75a5bf78b130e7200220275e4193cd3a74e228d97e66c2793735ac2b10a21a61df2a920c0bbfbedca9770121036735f72d6aee497b862bf9d14c92dac211493cdf86013d2023426dc18fa3f2920247304402204285443e3037bf313511de43e808f636a87c6928c740a585742c5cdedc39117302204a98f981b5a1201dd284de79c9e104d83891c0a446831bc734f816aa23aa7c1a0121036735f72d6aee497b862bf9d14c92dac211493cdf86013d2023426dc18fa3f2920247304402207caea07e1e7bbc5b5c9f978af43371b2eea48ea0f8322bd646458b227d2d8ed302206e38f5efc1c9d1ff7965fc48316af31ab63552aa498f91a6f3782bb4b8ec991b0121036735f72d6aee497b862bf9d14c92dac211493cdf86013d2023426dc18fa3f29228411a00")
	vout := 1

	// Creat root tx
	rootTx := wire.NewMsgTx(2)
	rootTx.AddTxIn(wire.NewTxIn(
		&wire.OutPoint{Hash: depositTx.TxHash(), Index: uint32(vout)},
		depositTx.TxOut[0].PkScript,
		nil, // witness
	))
	rootTx.AddTxOut(wire.NewTxOut(100_000, depositTx.TxOut[vout].PkScript))
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

	treeResponse, _ := client.StartTreeCreation(context.Background(), &pb.StartTreeCreationRequest{
		IdentityPublicKey: userPubkey,
		OnChainUtxo: &pb.UTXO{
			Txid: depositTx.TxID(),
			Vout: uint32(vout),
		},
		RootTxSigningJob: &pb.SigningJob{
			RawTxHex:               hex.EncodeToString(rootBuf.Bytes()),
			SigningPublicKey:       userPubkey,
			SigningNonceCommitment: &rootNonceCommitment,
		},
		RefundTxSigningJob: &pb.SigningJob{
			RawTxHex:               hex.EncodeToString(refundBuf.Bytes()),
			SigningPublicKey:       userPubkey,
			SigningNonceCommitment: &refundNonceCommitment,
		},
	})
	if treeResponse.TreeId == "" {
		t.Fatalf("failed to start tree creation")
	}
}
