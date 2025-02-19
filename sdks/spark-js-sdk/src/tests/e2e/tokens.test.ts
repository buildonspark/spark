import { numberToBytesBE } from "@noble/curves/abstract/utils";
import { secp256k1 } from "@noble/curves/secp256k1";
import { TokenTransaction } from "../../proto/spark";
import { TokenLeafCreationData } from "../../services/tokens";
import { SparkWallet } from "../../spark-sdk";
import { Network } from "../../utils/network";
import { hashTokenTransaction } from "../../utils/tokens";

describe("token integration test", () => {
  // Skip all tests if running in GitHub Actions
  const testFn = process.env.GITHUB_ACTIONS ? it.skip : it;

  it("should issue a single token", async () => {
    const tokenAmount: bigint = 1000n;

    const sdk = new SparkWallet(Network.REGTEST);
    const mnemonic = await sdk.generateMnemonic();
    await sdk.createSparkWallet(mnemonic);

    const pubKey = await sdk.getSigner().getIdentityPublicKey();

    const tokenLeafData: TokenLeafCreationData[] = [
      {
        tokenPublicKey: pubKey,
        tokenAmount: numberToBytesBE(tokenAmount, 16),
        withdrawalBondSats: 10000,
        withdrawalLocktime: Math.floor(Date.now() / 1000) + 24 * 60 * 60,
      },
    ];

    const tokenTransaction: TokenTransaction = {
      tokenInput: {
        $case: "mintInput",
        mintInput: {
          issuerPublicKey: pubKey,
          issuerProvidedTimestamp: Math.floor(Date.now() / 1000),
        },
      },
      outputLeaves: tokenLeafData.map((leafData) => ({
        id: crypto.randomUUID(),
        ownerPublicKey: pubKey,
        revocationPublicKey: new Uint8Array(0), // Will later be filled in
        withdrawalBondSats: leafData.withdrawalBondSats,
        withdrawalLocktime: leafData.withdrawalLocktime,
        tokenPublicKey: pubKey,
        tokenAmount: leafData.tokenAmount,
      })),
      sparkOperatorIdentityPublicKeys: [],
    };

    await sdk.broadcastTokenTransaction(tokenTransaction);
  });

  it("should issue multiple tokens", async () => {
    const tokenAmount: bigint = 3000n;
    const tokenAmount2: bigint = 2000n;

    const sdk = new SparkWallet(Network.REGTEST);
    const mnemonic = await sdk.generateMnemonic();
    await sdk.createSparkWallet(mnemonic);

    const pubKey = await sdk.getSigner().getIdentityPublicKey();

    const tokenLeafData: TokenLeafCreationData[] = [
      {
        tokenPublicKey: pubKey,
        tokenAmount: numberToBytesBE(tokenAmount, 32),
        withdrawalBondSats: 10000,
        withdrawalLocktime: Math.floor(Date.now() / 1000) + 24 * 60 * 60,
      },
      {
        tokenPublicKey: pubKey,
        tokenAmount: numberToBytesBE(tokenAmount2, 32),
        withdrawalBondSats: 10000,
        withdrawalLocktime: Math.floor(Date.now() / 1000) + 24 * 60 * 60,
      },
    ];

    const tokenTransaction: TokenTransaction = {
      tokenInput: {
        $case: "mintInput",
        mintInput: {
          issuerPublicKey: pubKey,
          issuerProvidedTimestamp: Math.floor(Date.now() / 1000),
        },
      },
      outputLeaves: tokenLeafData.map((leafData) => ({
        id: crypto.randomUUID(),
        ownerPublicKey: pubKey,
        withdrawalBondSats: leafData.withdrawalBondSats,
        withdrawalLocktime: leafData.withdrawalLocktime,
        tokenPublicKey: leafData.tokenPublicKey,
        tokenAmount: leafData.tokenAmount,
        revocationPublicKey: new Uint8Array(0), // Will be filled in later
      })),
      sparkOperatorIdentityPublicKeys: [],
    };

    await sdk.broadcastTokenTransaction(tokenTransaction);
  });

  it("should issue a single token and transfer it", async () => {
    const tokenAmount: bigint = 1000n;

    const sdk = new SparkWallet(Network.REGTEST);
    const mnemonic = await sdk.generateMnemonic();
    await sdk.createSparkWallet(mnemonic);

    const targetWalletPubKey = await sdk.getSigner().generatePublicKey();
    const pubKey = await sdk.getSigner().getIdentityPublicKey();

    const leafOwnerPrivateKey = secp256k1.utils.randomPrivateKey();
    const leafOwnerPublicKey = secp256k1.getPublicKey(leafOwnerPrivateKey);

    const issueTokenTransaction: TokenTransaction = {
      tokenInput: {
        $case: "mintInput",
        mintInput: {
          issuerPublicKey: pubKey,
          issuerProvidedTimestamp: Math.floor(Date.now() / 1000),
        },
      },
      outputLeaves: [
        {
          id: crypto.randomUUID(),
          ownerPublicKey: leafOwnerPublicKey,
          revocationPublicKey: new Uint8Array(0), // Will later be filled in
          withdrawBondSats: 10000,
          withdrawRelativeBlockLocktime:
            Math.floor(Date.now() / 1000) + 24 * 60 * 60,
          tokenPublicKey: pubKey,
          tokenAmount: numberToBytesBE(tokenAmount, 16),
        },
      ],
      sparkOperatorIdentityPublicKeys: [],
    };

    const finalIssuenceTransaction = await sdk.broadcastTokenTransaction(
      issueTokenTransaction
    );
    const finalTokenTransactionHash = hashTokenTransaction(
      finalIssuenceTransaction
    );

    const transferTokenTransaction: TokenTransaction = {
      tokenInput: {
        $case: "transferInput",
        transferInput: {
          leavesToSpend: finalIssuenceTransaction.outputLeaves.map(
            (leaf, index) => ({
              prevTokenTransactionHash: finalTokenTransactionHash,
              prevTokenTransactionLeafVout: index,
            })
          ),
        },
      },
      outputLeaves: [
        {
          id: crypto.randomUUID(),
          ownerPublicKey: targetWalletPubKey,
          revocationPublicKey: new Uint8Array(0), // Will later be filled in
          withdrawBondSats: 10000,
          withdrawRelativeBlockLocktime:
            Math.floor(Date.now() / 1000) + 24 * 60 * 60,
          tokenPublicKey: pubKey,
          tokenAmount: numberToBytesBE(tokenAmount, 16),
        },
      ],
      sparkOperatorIdentityPublicKeys: [],
    };

    const finalRevocationPublicKey = new Uint8Array(
      finalIssuenceTransaction.outputLeaves[0].revocationPublicKey ||
        new Uint8Array(0)
    );
    await sdk.broadcastTokenTransaction(
      transferTokenTransaction,
      [leafOwnerPrivateKey],
      [finalRevocationPublicKey]
    );
  });
});
