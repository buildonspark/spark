import { SparkWallet } from "../spark-sdk";
import { getTestWalletConfig } from "./test-util";
import { bytesToHex, hexToBytes } from "@noble/curves/abstract/utils";
import { createMockGrpcConnection } from "../utils/connection";
import { secp256k1 } from "@noble/curves/secp256k1";
import { getTxFromRawTxBytes, getTxId } from "../utils/bitcoin";
import { createDummyTx } from "../utils/wasm";

describe("deposit", () => {
  // Skip all tests if running in GitHub Actions
  const testFn = process.env.GITHUB_ACTIONS ? it.skip : it;

  testFn("should generate a deposit address", async () => {
    const config = getTestWalletConfig();
    const sdk = new SparkWallet(config);
    const pubKey = hexToBytes(
      "0330d50fd2e26d274e15f3dcea34a8bb611a9d0f14d1a9b1211f3608b3b7cd56c7"
    );
    const depositAddress = await sdk.generateDepositAddress(pubKey);

    expect(depositAddress.depositAddress).toBeDefined();
  });

  testFn("should create a tree root", async () => {
    const config = getTestWalletConfig();
    const sdk = new SparkWallet(config);

    // Setup mock connection
    const mockClient = createMockGrpcConnection(
      config.signingOperators[config.coodinatorIdentifier].address
    );

    // Generate private/public key pair
    const privKey = secp256k1.utils.randomPrivateKey();
    const pubKey = secp256k1.getPublicKey(privKey);

    // Generate deposit address
    const depositResp = await sdk.generateDepositAddress(pubKey);
    if (!depositResp.depositAddress) {
      throw new Error("deposit address not found");
    }

    const dummyTx = createDummyTx({
      address: depositResp.depositAddress.address,
      amountSats: 100_000n,
    });

    const depositTxHex = bytesToHex(dummyTx.tx);
    const depositTx = getTxFromRawTxBytes(dummyTx.tx);

    const vout = 0;
    const txid = getTxId(depositTx);
    if (!txid) {
      throw new Error("txid not found");
    }

    // Set mock transaction
    await mockClient.set_mock_onchain_tx({
      txid,
      tx: depositTxHex,
    });

    // Create tree root
    const treeResp = await sdk.createTreeRoot(
      privKey,
      depositResp.depositAddress.verifyingKey,
      depositTx,
      vout
    );

    console.log("tree created:", treeResp);

    mockClient.close();
  });
});
