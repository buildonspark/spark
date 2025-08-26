import { describe, expect, it } from "@jest/globals";
import { secp256k1 } from "@noble/curves/secp256k1";
import { hexToBytes } from "@noble/curves/utils";
import { Address, OutScript, Transaction } from "@scure/btc-signer";
import { TransactionInput } from "@scure/btc-signer/psbt";
import { equalBytes } from "@scure/btc-signer/utils";
import { uuidv7 } from "uuidv7";
import { WalletConfigService } from "../../services/config.js";
import { ConnectionManager } from "../../services/connection.js";
import { CoopExitService } from "../../services/coop-exit.js";
import { SigningService } from "../../services/signing.js";
import type { LeafKeyTweak } from "../../services/transfer.js";
import { TransferService } from "../../services/transfer.js";
import { ConfigOptions } from "../../services/wallet-config.js";
import {
  getP2TRAddressFromPublicKey,
  getP2TRScriptFromPublicKey,
  getTxId,
  getTxIdNoReverse,
} from "../../utils/bitcoin.js";
import { getNetwork, Network } from "../../utils/network.js";
import { walletTypes } from "../test-utils.js";
import { SparkWalletTesting } from "../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../utils/test-faucet.js";

import { KeyDerivation, KeyDerivationType } from "../../index.js";
import { TransferStatus } from "../../proto/spark.js";
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
    const connectionManager = new ConnectionManager(configService);
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
    const sspConnectionManager = new ConnectionManager(sspConfigService);
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
    exitTx.addInput(faucetCoin!.outpoint);
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

    const connectorTx = new Transaction();
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
    };

    const senderTransfer = await coopExitService.getConnectorRefundSignatures({
      leaves: [transferNode],
      exitTxId: hexToBytes(getTxIdNoReverse(exitTx)),
      connectorOutputs,
      receiverPubKey: hexToBytes(sspPubkey),
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
    const leavesToClaim: LeafKeyTweak[] = receiverTransfer!.leaves.map(
      (leaf) => ({
        leaf: leaf.leaf!,
        keyDerivation: {
          type: KeyDerivationType.ECIES,
          path: leaf.secretCipher,
        },
        newKeyDerivation: {
          type: KeyDerivationType.LEAF,
          path: leaf.leaf!.id,
        },
      }),
    );

    let hasError = false;
    try {
      await sspTransferService.claimTransfer(receiverTransfer!, leavesToClaim);
    } catch (e) {
      hasError = true;
    }
    expect(hasError).toBe(true);

    // Sign an exit tx and broadcast
    const signedExitTx = await faucet.signFaucetCoin(
      exitTx,
      faucetCoin!.txout,
      faucetCoin!.key,
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

    // Sleep to allow the SO chain watcher to catch up
    await new Promise((resolve) => setTimeout(resolve, 1000));

    // Refetch and claim incoming transfer
    const transfers = await sspWallet.queryPendingTransfers();
    expect(transfers.transfers.length).toBe(1);
    await sspTransferService.claimTransfer(
      transfers.transfers[0]!,
      leavesToClaim,
    );
  }, 30000);
});
