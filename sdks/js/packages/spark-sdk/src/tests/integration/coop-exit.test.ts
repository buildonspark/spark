import { describe, expect, it, jest } from "@jest/globals";
import { secp256k1 } from "@noble/curves/secp256k1";
import { bytesToHex, hexToBytes } from "@noble/curves/utils";
import { Address, OutScript, Transaction } from "@scure/btc-signer";
import { type TransactionInput } from "@scure/btc-signer/psbt";
import { equalBytes } from "@scure/btc-signer/utils";
import { uuidv7 } from "uuidv7";
import { SparkValidationError } from "../../errors/index.js";
import { TransferStatus } from "../../proto/spark.js";
import { WalletConfigService } from "../../services/config.js";
import { ConnectionManagerNodeJS } from "../../services/connection/connection.node.js";
import { CoopExitService } from "../../services/coop-exit.js";
import { SigningService } from "../../services/signing.js";
import type { LeafKeyTweak } from "../../services/transfer.js";
import { TransferService } from "../../services/transfer.js";
import { type ConfigOptions } from "../../services/wallet-config.js";
import { DefaultSparkSigner } from "../../signer/signer.js";
import { type KeyDerivation, KeyDerivationType } from "../../signer/types.js";
import { ExitSpeed } from "../../types/index.js";
import {
  getP2TRAddressFromPublicKey,
  getP2TRScriptFromPublicKey,
  getTxId,
} from "../../utils/bitcoin.js";
import { getNetwork, Network } from "../../utils/network.js";
import { createNewTree, walletTypes } from "../test-utils.js";
import {
  SparkWalletTesting,
  SparkWalletTestingIntegration,
} from "../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../utils/test-faucet.js";
import { retryUntilSuccess } from "../utils/utils.js";

describe.each(walletTypes)("coop exit", ({ name, Signer, createTree }) => {
  it(`${name} - test coop exit`, async () => {
    const faucet = BitcoinFaucet.getInstance();

    const faucetCoin = await faucet.fund();

    const amountSats = 100_000n;

    const options: ConfigOptions = {
      network: "LOCAL",
    };

    // Setup user with leaves
    const { wallet: userWallet } = await SparkWalletTesting.initialize({
      options,
      signer: new Signer(),
    });

    const configService = new WalletConfigService(
      options,
      userWallet.getSigner(),
    );
    const connectionManager = new ConnectionManagerNodeJS(configService);
    const signingService = new SigningService(configService);
    const coopExitService = new CoopExitService(
      configService,
      connectionManager,
      signingService,
    );

    const leafId = uuidv7();
    const rootNode = await createTree(userWallet, leafId, faucet, amountSats);

    // Setup ssp
    const { wallet: sspWallet } = await SparkWalletTesting.initialize({
      options,
      signer: new Signer(),
    });
    const sspPubkey = await sspWallet.getIdentityPublicKey();

    const sspConfigService = new WalletConfigService(
      options,
      sspWallet.getSigner(),
    );
    const sspConnectionManager = new ConnectionManagerNodeJS(sspConfigService);
    const sspSigningService = new SigningService(sspConfigService);
    const sspTransferService = new TransferService(
      sspConfigService,
      sspConnectionManager,
      sspSigningService,
    );

    const sspIntermediateAddressScript = getP2TRScriptFromPublicKey(
      hexToBytes(sspPubkey),
      Network.LOCAL,
    );

    // Setup withdraw
    const randomWithdrawKey = secp256k1.utils.randomPrivateKey();
    const withdrawPubKey = secp256k1.getPublicKey(randomWithdrawKey);
    const withdrawAddressScript = getP2TRScriptFromPublicKey(
      withdrawPubKey,
      Network.LOCAL,
    );

    const leafCount = 1;
    const dustAmountSats = 354;
    const intermediateAmountSats = (leafCount + 1) * dustAmountSats;

    const exitTx = new Transaction();
    exitTx.addInput(faucetCoin.outpoint);
    exitTx.addOutput({
      script: withdrawAddressScript,
      amount: amountSats,
    });
    exitTx.addOutput({
      script: sspIntermediateAddressScript,
      amount: BigInt(intermediateAmountSats),
    });

    const exitTxId = getTxId(exitTx);
    const intermediateOutPoint: TransactionInput = {
      txid: hexToBytes(exitTxId),
      index: 1,
    };

    let connectorP2trAddrs: string[] = [];
    for (let i = 0; i < leafCount + 1; i++) {
      const randomConnectorKey = secp256k1.utils.randomPrivateKey();
      const connectorPubKey = secp256k1.getPublicKey(randomConnectorKey);
      const connectorP2trAddr = getP2TRAddressFromPublicKey(
        connectorPubKey,
        Network.LOCAL,
      );
      connectorP2trAddrs.push(connectorP2trAddr);
    }
    const feeBumpAddr = connectorP2trAddrs[connectorP2trAddrs.length - 1];
    connectorP2trAddrs = connectorP2trAddrs.slice(0, -1);

    const connectorTx = new Transaction({ version: 3 });
    connectorTx.addInput(intermediateOutPoint);
    for (const addr of [...connectorP2trAddrs, feeBumpAddr]) {
      connectorTx.addOutput({
        script: OutScript.encode(
          Address(getNetwork(Network.LOCAL)).decode(addr!),
        ),
        amount: BigInt(intermediateAmountSats / connectorP2trAddrs.length),
      });
    }

    const connectorOutputs: TransactionInput[] = [];
    for (let i = 0; i < connectorTx.outputsLength - 1; i++) {
      connectorOutputs.push({
        txid: hexToBytes(getTxId(connectorTx)),
        index: i,
      });
    }

    const newLeafDerivationPath: KeyDerivation = {
      type: KeyDerivationType.LEAF,
      path: uuidv7(),
    };
    const transferNode: LeafKeyTweak = {
      leaf: rootNode,
      keyDerivation: {
        type: KeyDerivationType.LEAF,
        path: leafId,
      },
      newKeyDerivation: newLeafDerivationPath,
      receiverIdentityPublicKey: hexToBytes(sspPubkey),
    };

    const transferId = uuidv7();
    const senderTransfer = await coopExitService.getConnectorRefundSignatures({
      leaves: [transferNode],
      exitTxId: hexToBytes(getTxId(exitTx)),
      connectorOutputs,
      receiverPubKey: hexToBytes(sspPubkey),
      transferId,
      connectorTx: connectorTx.toBytes(),
    });

    const receiverTransfer = await sspTransferService.queryTransfer(
      senderTransfer.transfer.id,
    );

    expect(receiverTransfer!.id).toBe(senderTransfer.transfer.id);
    expect(receiverTransfer!.status).toBe(
      TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING,
    );

    const leafPubKeyMap = await sspWallet.verifyPendingTransfer(
      receiverTransfer!,
    );

    expect(leafPubKeyMap.size).toBe(1);
    expect(leafPubKeyMap.get(rootNode.id)).toBeDefined();
    expect(
      equalBytes(
        leafPubKeyMap.get(rootNode.id)!,
        await userWallet
          .getSigner()
          .getPublicKeyFromDerivation(newLeafDerivationPath),
      ),
    ).toBe(true);

    // Try to claim leaf before exit tx confirms -> should fail

    let hasError = false;
    try {
      await sspTransferService.claimTransfer(receiverTransfer!);
    } catch {
      hasError = true;
    }
    expect(hasError).toBe(true);

    // Sign an exit tx and broadcast
    const signedExitTx = await faucet.signFaucetCoin(
      exitTx,
      faucetCoin.txout,
      faucetCoin.key,
    );

    await faucet.broadcastTx(signedExitTx.hex);

    // Make sure the exit tx gets enough confirmations
    const randomKey = secp256k1.utils.randomPrivateKey();
    const randomPubKey = secp256k1.getPublicKey(randomKey);
    const randomAddress = getP2TRAddressFromPublicKey(
      randomPubKey,
      Network.LOCAL,
    );
    // Confirm extra buffer to scan more blocks than needed
    // So that we don't race the chain watcher in this test
    await faucet.generateToAddress(30, randomAddress);

    // Generate 2 more blocks for key tweaking requirement (knob requires 2+ confirmations)
    await faucet.generateToAddress(2, randomAddress);

    // Sleep to allow the SO chain watcher to catch up and process key tweaking
    await new Promise((resolve) => setTimeout(resolve, 3000));

    // Refetch and claim incoming transfer - retry with backoff to handle timing
    let transfers = await sspWallet.queryPendingTransfers();
    let retries = 0;
    while (transfers.transfers.length === 0 && retries < 5) {
      await new Promise((resolve) => setTimeout(resolve, 2000));
      transfers = await sspWallet.queryPendingTransfers();
      retries++;
    }
    expect(transfers.transfers.length).toBe(1);
    await sspTransferService.claimTransfer(transfers.transfers[0]!);
  }, 30000);
});

describe("malicious SSP cooperative exit rejection", () => {
  it("SDK refuses to sign when SSP-supplied L1 tx pays a different address", async () => {
    const faucet = BitcoinFaucet.getInstance();
    const amountSats = 100_000n;
    const feeAmountSats = 1_000;

    const { wallet: userWallet } =
      await SparkWalletTestingIntegration.initialize({
        options: { network: "LOCAL" },
        signer: new DefaultSparkSigner(),
      });

    const leafId = uuidv7();
    await createNewTree(userWallet, leafId, faucet, amountSats);

    const initialBalance = await userWallet.getBalance();
    expect(initialBalance.balance).toBe(amountSats);

    const requestedWithdrawKey = secp256k1.utils.randomPrivateKey();
    const requestedWithdrawPubKey =
      secp256k1.getPublicKey(requestedWithdrawKey);
    const requestedWithdrawalAddress = getP2TRAddressFromPublicKey(
      requestedWithdrawPubKey,
      Network.LOCAL,
    );

    const attackerKey = secp256k1.utils.randomPrivateKey();
    const attackerPubKey = secp256k1.getPublicKey(attackerKey);
    const attackerScript = getP2TRScriptFromPublicKey(
      attackerPubKey,
      Network.LOCAL,
    );

    const sspIdentityPublicKey = userWallet
      .getConfigService()
      .getSspIdentityPublicKey();

    const maliciousResponse = await buildMaliciousCoopExitResponse({
      faucet,
      attackerScript,
      sspIdentityPublicKey,
      payoutAmountSats: Number(amountSats) - feeAmountSats,
    });

    const maliciousSspClient = {
      requestCoopExit: jest.fn(() => Promise.resolve(maliciousResponse)),
      completeCoopExit: jest.fn(() => Promise.resolve(maliciousResponse)),
      getTransfers: jest.fn(() => Promise.resolve([])),
    };

    (userWallet as unknown as { sspClient: unknown }).sspClient =
      maliciousSspClient;

    await expect(
      userWallet.withdraw({
        amountSats: Number(amountSats),
        onchainAddress: requestedWithdrawalAddress,
        feeAmountSats,
        feeQuoteId: "malicious-ssp-fee-quote",
        exitSpeed: ExitSpeed.FAST,
        deductFeeFromWithdrawalAmount: true,
      }),
    ).rejects.toThrow(SparkValidationError);

    expect(maliciousSspClient.requestCoopExit).toHaveBeenCalledTimes(1);
    expect(maliciousSspClient.completeCoopExit).toHaveBeenCalledTimes(0);

    const balanceAfter = await userWallet.getBalance();
    expect(balanceAfter.balance).toBe(amountSats);
  }, 600_000);
});

describe("malicious SSP coop-exit rejection (SDK-side binding check)", () => {
  it("withdraw() rejects when coopExitTxid does not bind to connector_tx", async () => {
    const faucet = BitcoinFaucet.getInstance();
    const amountSats = 100_000n;
    const feeAmountSats = 1_000;

    const options: ConfigOptions = { network: "LOCAL" };
    const { wallet: userWallet } = await SparkWalletTesting.initialize({
      options,
      signer: new DefaultSparkSigner(),
    });

    // Fund the wallet so withdraw() has a leaf to send. createNewTree only
    // returns once the deposit tx is mined; wait for the wallet's
    // background sync to ingest the leaf so getBalance() reflects it.
    const leafId = uuidv7();
    await createNewTree(userWallet, leafId, faucet, amountSats);
    await retryUntilSuccess(async () => {
      const { balance } = await userWallet.getBalance();
      if (balance !== amountSats) {
        throw new Error(`balance not yet ${amountSats}, got ${balance}`);
      }
      return balance;
    });

    // Build a structurally-valid rawConnectorTransaction whose first input
    // spends an arbitrary "legit" parent hash. The connector tx itself
    // never has to be broadcast; we only need it to parse and have a
    // first input.
    const legitParent = new Uint8Array(32).fill(0).map((_, i) => i + 1);
    const connectorTx = new Transaction({ allowUnknownOutputs: true });
    connectorTx.addInput({ txid: legitParent, index: 0 });
    connectorTx.addOutput({
      script: new Uint8Array([0x51]),
      amount: 354n,
    });

    // The "alibi" coopExitTxid the SSP returns: 32 bytes that do NOT match
    // legitParent in either endianness. This is the request shape that the
    // SO validator (parseAndValidateCoopExitTxid) catches; the SDK should
    // refuse to make the SO RPC at all.
    const alibiCoopExitTxid = new Uint8Array(32).fill(0).map((_, i) => 255 - i);

    const withdrawalAddress = getP2TRAddressFromPublicKey(
      secp256k1.getPublicKey(secp256k1.utils.randomPrivateKey()),
      Network.LOCAL,
    );

    const maliciousResponse = {
      coopExitTxid: bytesToHex(alibiCoopExitTxid),
      rawConnectorTransaction: bytesToHex(connectorTx.toBytes()),
      rawCoopExitTransaction: bytesToHex(connectorTx.toBytes()),
      id: uuidv7(),
    };

    const requestCoopExit = jest.fn(() => Promise.resolve(maliciousResponse));
    const completeCoopExit = jest.fn();
    (
      userWallet as unknown as {
        sspClient: { requestCoopExit: unknown; completeCoopExit: unknown };
      }
    ).sspClient = {
      requestCoopExit,
      completeCoopExit,
    };

    await expect(
      userWallet.withdraw({
        amountSats: Number(amountSats),
        feeAmountSats,
        // feeQuoteId is forwarded to the (stubbed) sspClient so any
        // non-empty value gets us past the SDK's early validation.
        feeQuoteId: "test-fee-quote-id",
        onchainAddress: withdrawalAddress,
        exitSpeed: ExitSpeed.FAST,
        deductFeeFromWithdrawalAmount: true,
      }),
    ).rejects.toThrow(SparkValidationError);

    expect(requestCoopExit).toHaveBeenCalledTimes(1);
    // No completion RPC should fire when the SDK rejects upstream.
    expect(completeCoopExit).not.toHaveBeenCalled();

    // Balance must be unchanged: no SO RPC, no refund signing, no leaf
    // state mutation.
    const { balance } = await userWallet.getBalance();
    expect(balance).toBe(amountSats);
  }, 60000);
});

async function buildMaliciousCoopExitResponse({
  faucet,
  attackerScript,
  sspIdentityPublicKey,
  payoutAmountSats,
}: {
  faucet: BitcoinFaucet;
  attackerScript: Uint8Array;
  sspIdentityPublicKey: string;
  payoutAmountSats: number;
}) {
  const faucetCoin = await faucet.fund();
  const sspIntermediateAddressScript = getP2TRScriptFromPublicKey(
    hexToBytes(sspIdentityPublicKey),
    Network.LOCAL,
  );

  const leafCount = 1;
  const dustAmountSats = 354;
  const intermediateAmountSats = (leafCount + 1) * dustAmountSats;

  const exitTx = new Transaction();
  exitTx.addInput(faucetCoin.outpoint);
  exitTx.addOutput({
    script: attackerScript,
    amount: BigInt(payoutAmountSats),
  });
  exitTx.addOutput({
    script: sspIntermediateAddressScript,
    amount: BigInt(intermediateAmountSats),
  });

  const exitTxId = getTxId(exitTx);
  const intermediateOutPoint: TransactionInput = {
    txid: hexToBytes(exitTxId),
    index: 1,
  };

  const connectorP2trAddrs: string[] = [];
  for (let i = 0; i < leafCount + 1; i++) {
    const randomConnectorKey = secp256k1.utils.randomPrivateKey();
    const connectorPubKey = secp256k1.getPublicKey(randomConnectorKey);
    connectorP2trAddrs.push(
      getP2TRAddressFromPublicKey(connectorPubKey, Network.LOCAL),
    );
  }

  const connectorTx = new Transaction({ version: 3 });
  connectorTx.addInput(intermediateOutPoint);
  for (const addr of connectorP2trAddrs) {
    connectorTx.addOutput({
      script: OutScript.encode(Address(getNetwork(Network.LOCAL)).decode(addr)),
      amount: BigInt(dustAmountSats),
    });
  }

  return {
    rawCoopExitTransaction: bytesToHex(exitTx.toBytes(true)),
    rawConnectorTransaction: bytesToHex(connectorTx.toBytes(true)),
    coopExitTxid: exitTxId,
  };
}
