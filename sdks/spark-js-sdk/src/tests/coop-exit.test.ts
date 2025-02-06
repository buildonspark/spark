import { equalBytes, hexToBytes } from "@noble/curves/abstract/utils";
import { Address, OutScript, Transaction } from "@scure/btc-signer";
import { TransactionInput } from "@scure/btc-signer/psbt";
import { sha256 } from "@scure/btc-signer/utils";
import { WalletConfigService } from "../services/config";
import { ConnectionManager } from "../services/connection";
import { CoopExitService } from "../services/coop-exit";
import { LeafKeyTweak, TransferService } from "../services/transfer";
import { SparkWallet } from "../spark-sdk";
import {
  getP2TRAddressFromPublicKey,
  getP2TRScriptFromPublicKey,
  getTxFromRawTxBytes,
  getTxId,
  getTxIdNoReverse,
} from "../utils/bitcoin";
import { getNetwork, Network } from "../utils/network";
import { createDummyTx } from "../utils/wasm";
import { createNewTree } from "./test-util";

describe("coop exit", () => {
  // Skip all tests if running in GitHub Actions
  const testFn = process.env.GITHUB_ACTIONS ? it.skip : it;

  testFn("test coop exit", async () => {
    const wallet = new SparkWallet(Network.REGTEST);
    const mnemonic = wallet.generateMnemonic();
    await wallet.createSparkWallet(mnemonic);

    const config = wallet.getConfig();

    const configService = new WalletConfigService(
      Network.REGTEST,
      wallet.getSigner()
    );
    const connectionManager = new ConnectionManager(configService);
    const transferService = new TransferService(
      configService,
      connectionManager
    );
    const coopExitService = new CoopExitService(
      configService,
      connectionManager,
      transferService
    );

    const leafPubKey = wallet.getSigner().generatePublicKey(sha256("asdf"));
    const rootNode = await createNewTree(wallet, leafPubKey);

    const sspWallet = new SparkWallet(Network.REGTEST);
    const sspMnemonic = sspWallet.generateMnemonic();
    const sspPubkey = await sspWallet.createSparkWallet(sspMnemonic);

    const sspIntermediateAddressScript = getP2TRScriptFromPublicKey(
      hexToBytes(sspPubkey),
      config.network
    );
    const amountSats = 100_000n;

    const withdrawPubKey = wallet.getSigner().generatePublicKey();
    const withdrawAddress = getP2TRAddressFromPublicKey(
      withdrawPubKey,
      config.network
    );

    const leafCount = 1;
    const dustAmountSats = 354;
    const intermediateAmountSats = (leafCount + 1) * dustAmountSats;

    const dummyTx = createDummyTx({
      address: withdrawAddress,
      amountSats,
    });

    const exitTx = getTxFromRawTxBytes(dummyTx.tx);

    exitTx.addOutput({
      script: sspIntermediateAddressScript,
      amount: BigInt(intermediateAmountSats),
    });

    const intermediateInput: TransactionInput = {
      txid: hexToBytes(getTxId(exitTx)),
      index: 1,
    };
    let connectorP2trAddrs: string[] = [];
    for (let i = 0; i < leafCount + 1; i++) {
      const connectorPubKey = wallet.getSigner().generatePublicKey();
      const connectorP2trAddr = getP2TRAddressFromPublicKey(
        connectorPubKey,
        config.network
      );
      connectorP2trAddrs.push(connectorP2trAddr);
    }

    const feeBumpAddr = connectorP2trAddrs[connectorP2trAddrs.length - 1];
    connectorP2trAddrs = connectorP2trAddrs.slice(0, -1);
    const transaction = new Transaction();
    transaction.addInput(intermediateInput);

    for (const addr of [...connectorP2trAddrs, feeBumpAddr]) {
      transaction.addOutput({
        script: OutScript.encode(
          Address(getNetwork(config.network)).decode(addr)
        ),
        amount: BigInt(
          intermediateAmountSats / (connectorP2trAddrs.length + 1)
        ),
      });
    }

    const connectorOutputs = [];
    for (let i = 0; i < transaction.outputsLength - 1; i++) {
      connectorOutputs.push({
        txid: hexToBytes(getTxId(transaction)),
        index: i,
      });
    }

    const newLeafPubKey = wallet.getSigner().generatePublicKey();

    const senderTransfer = await coopExitService.getConnectorRefundSignatures({
      leaves: [
        {
          leaf: rootNode,
          signingPubKey: leafPubKey,
          newSigningPubKey: newLeafPubKey,
        },
      ],
      exitTxId: hexToBytes(getTxId(exitTx)),
      connectorOutputs,
      receiverPubKey: hexToBytes(sspPubkey),
    });

    const pendingTransfer = await sspWallet.queryPendingTransfers();

    expect(pendingTransfer.transfers.length).toBe(1);

    const receiverTransfer = pendingTransfer.transfers[0];
    expect(receiverTransfer.id).toBe(senderTransfer.transfer.id);
    expect(receiverTransfer.leaves[0].leaf).toBeDefined();
    const leafPubKeyMap = await sspWallet.verifyPendingTransfer(
      receiverTransfer
    );

    expect(leafPubKeyMap.size).toBe(1);
    expect(leafPubKeyMap.get(rootNode.id)).toBeDefined();
    const isEqual = equalBytes(leafPubKeyMap.get(rootNode.id)!, newLeafPubKey);

    expect(isEqual).toBe(true);
    const finalLeafPubKey = sspWallet.getSigner().generatePublicKey();

    const leavesToClaim: LeafKeyTweak[] = [
      {
        leaf: receiverTransfer.leaves[0].leaf!,
        signingPubKey: newLeafPubKey,
        newSigningPubKey: finalLeafPubKey,
      },
    ];

    let hasError = false;
    try {
      await sspWallet.claimTransfer(receiverTransfer, leavesToClaim);
    } catch (e) {
      hasError = true;
    }
    expect(hasError).toBe(true);
    for (const signingOperator of Object.values(config.signingOperators)) {
      const mockConnection = ConnectionManager.createMockClient(
        signingOperator.address
      );
      await mockConnection.set_mock_onchain_tx({
        // Weird thing with backend go serialization
        // Since this is a mock tx, should be fine
        txid: getTxIdNoReverse(exitTx),
        tx: exitTx.hex,
      });
    }

    await sspWallet.claimTransfer(receiverTransfer, leavesToClaim);
  });
});
