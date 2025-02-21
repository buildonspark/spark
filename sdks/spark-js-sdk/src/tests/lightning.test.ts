import { afterEach, beforeAll, describe, expect, it, xit } from "@jest/globals";
import { hexToBytes } from "@noble/curves/abstract/utils";
import { equalBytes, sha256 } from "@scure/btc-signer/utils";
import { TransferStatus } from "../proto/spark.js";
import { WalletConfigService } from "../services/config.js";
import { ConnectionManager } from "../services/connection.js";
import { LightningService } from "../services/lightning.js";
import { LeafKeyTweak, TransferService } from "../services/transfer.js";
import { SparkWallet } from "../spark-sdk.js";
import { Network } from "../utils/network.js";
import { createNewTree, getTestWalletConfig } from "./test-util.js";
import { BitcoinFaucet } from "./utils/test-faucet.js";

async function cleanUp() {
  const config = getTestWalletConfig();

  const preimage = hexToBytes(
    "2d059c3ede82a107aa1452c0bea47759be3c5c6e5342be6a310f6c3a907d9f4c"
  );
  const paymentHash = sha256(preimage);

  for (const operator of Object.values(config.signingOperators)) {
    const client = ConnectionManager.createMockClient(operator.address);
    await client.clean_up_preimage_share({
      paymentHash,
    });
    client.close();
  }
}

const fakeInvoiceCreator = async () => {
  return "lnbcrt123450n1pnj6uf4pp5l26hsdxssmr52vd4xmn5xran7puzx34hpr6uevaq7ta0ayzrp8esdqqcqzpgxqyz5vqrzjqtr2vd60g57hu63rdqk87u3clac6jlfhej4kldrrjvfcw3mphcw8sqqqqzp3jlj6zyqqqqqqqqqqqqqq9qsp5w22fd8aqn7sdum7hxdf59ptgk322fkv589ejxjltngvgehlcqcyq9qxpqysgqvykwsxdx64qrj0s5pgcgygmrpj8w25jsjgltwn09yp24l9nvghe3dl3y0ycy70ksrlqmcn42hxn24e0ucuy3g9fjltudvhv4lrhhamgq3stqgp";
};

describe("LightningService", () => {
  let userWallet: SparkWallet;
  let userConfig: WalletConfigService;
  let lightningService: LightningService;
  let transferService: TransferService;

  let sspWallet: SparkWallet;
  let sspConfig: WalletConfigService;
  let sspLightningService: LightningService;
  let sspTransferService: TransferService;

  // Skip all tests if running in GitHub Actions
  const testFn = process.env.GITHUB_ACTIONS ? it.skip : xit;

  beforeAll(async () => {
    userWallet = new SparkWallet(Network.REGTEST);
    const mnemonic = await userWallet.generateMnemonic();
    await userWallet.createSparkWallet(mnemonic);
    userConfig = new WalletConfigService(
      Network.REGTEST,
      userWallet.getSigner()
    );
    const connectionManager = new ConnectionManager(userConfig);
    lightningService = new LightningService(userConfig, connectionManager);
    transferService = new TransferService(userConfig, connectionManager);

    sspWallet = new SparkWallet(Network.REGTEST);
    const sspMnemonic = await sspWallet.generateMnemonic();
    await sspWallet.createSparkWallet(sspMnemonic);

    sspConfig = new WalletConfigService(Network.REGTEST, sspWallet.getSigner());
    const sspConnectionManager = new ConnectionManager(sspConfig);
    sspLightningService = new LightningService(sspConfig, sspConnectionManager);
    sspTransferService = new TransferService(sspConfig, sspConnectionManager);
  });
  afterEach(async () => {
    await cleanUp();
  });

  testFn("should create an invoice", async () => {
    const preimage = hexToBytes(
      "2d059c3ede82a107aa1452c0bea47759be3c5c6e5342be6a310f6c3a907d9f4c"
    );

    const invoice = await lightningService.createLightningInvoiceWithPreImage({
      invoiceCreator: fakeInvoiceCreator,
      amountSats: 100,
      memo: "test",
      preimage,
    });

    expect(invoice).toBeDefined();
  });

  testFn(
    "test receive lightning payment",
    async () => {
      const faucet = new BitcoinFaucet(
        "http://127.0.0.1:18443",
        "admin1",
        "123"
      );

      const preimage = hexToBytes(
        "2d059c3ede82a107aa1452c0bea47759be3c5c6e5342be6a310f6c3a907d9f4c"
      );
      const paymentHash = sha256(preimage);

      const invoice = await lightningService.createLightningInvoiceWithPreImage(
        {
          invoiceCreator: fakeInvoiceCreator,
          amountSats: 100,
          memo: "test",
          preimage,
        }
      );

      expect(invoice).toBeDefined();

      const sspLeafPubKey = await sspWallet.getSigner().generatePublicKey();
      const nodeToSend = await createNewTree(
        sspWallet,
        sspLeafPubKey,
        faucet,
        12345n
      );

      const newLeafPubKey = await sspWallet
        .getSigner()
        .generatePublicKey(sha256("1"));

      const leaves: LeafKeyTweak[] = [
        {
          leaf: nodeToSend,
          signingPubKey: sspLeafPubKey,
          newSigningPubKey: newLeafPubKey,
        },
      ];

      const response = await sspLightningService.swapNodesForPreimage({
        leaves,
        receiverIdentityPubkey: await userConfig.signer.getIdentityPublicKey(),
        paymentHash,
        isInboundPayment: true,
      });

      expect(equalBytes(response.preimage, preimage)).toBe(true);

      const senderTransfer = response.transfer;

      expect(senderTransfer).toBeDefined();

      const transfer = await sspTransferService.sendTransferTweakKey(
        senderTransfer!,
        leaves,
        new Map()
      );

      expect(transfer.status).toEqual(
        TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED
      );

      const pendingTransfer = await transferService.queryPendingTransfers();

      expect(pendingTransfer.transfers.length).toBe(1);

      const receiverTransfer = pendingTransfer.transfers[0];

      expect(receiverTransfer.id).toEqual(senderTransfer!.id);

      const leafPrivKeyMap = await transferService.verifyPendingTransfer(
        receiverTransfer
      );

      expect(leafPrivKeyMap.size).toBe(1);
      expect(leafPrivKeyMap.has(nodeToSend.id)).toBe(true);
      expect(
        equalBytes(leafPrivKeyMap.get(nodeToSend.id)!, newLeafPubKey)
      ).toBe(true);

      const finalLeafPubKey = await userWallet.getSigner().generatePublicKey();

      const leaf = receiverTransfer.leaves[0].leaf;
      expect(leaf).toBeDefined();

      const claimingNode = {
        leaf: leaf!,
        signingPubKey: newLeafPubKey,
        newSigningPubKey: finalLeafPubKey,
      };

      await transferService.claimTransfer(receiverTransfer, [claimingNode]);
    },
    60000
  );

  testFn(
    "test send lightning payment",
    async () => {
      const faucet = new BitcoinFaucet(
        "http://127.0.0.1:18443",
        "admin1",
        "123"
      );

      const preimage = hexToBytes(
        "2d059c3ede82a107aa1452c0bea47759be3c5c6e5342be6a310f6c3a907d9f4c"
      );
      const paymentHash = sha256(preimage);

      const userLeafPubKey = await userWallet
        .getSigner()
        .generatePublicKey(sha256("1"));
      const nodeToSend = await createNewTree(
        userWallet,
        userLeafPubKey,
        faucet,
        12345n
      );

      const newLeafPubKey = await userWallet
        .getSigner()
        .generatePublicKey(sha256("2"));

      const leaves: LeafKeyTweak[] = [
        {
          leaf: nodeToSend,
          signingPubKey: userLeafPubKey,
          newSigningPubKey: newLeafPubKey,
        },
      ];

      const response = await lightningService.swapNodesForPreimage({
        leaves,
        receiverIdentityPubkey: await sspConfig.signer.getIdentityPublicKey(),
        paymentHash,
        isInboundPayment: false,
        invoiceString: await fakeInvoiceCreator(),
      });

      expect(response.transfer).toBeDefined();

      // const refunds = await sspLightningService.queryUserSignedRefunds(
      //   paymentHash
      // );

      // let totalValue = 0n;
      // for (const refund of refunds) {
      //   const value = sspLightningService.validateUserSignedRefund(refund);
      //   totalValue += value;
      // }

      // expect(totalValue).toBe(12345n);

      const transfer = await transferService.sendTransferTweakKey(
        response.transfer!,
        leaves,
        new Map()
      );

      expect(transfer.status).toEqual(
        TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAK_PENDING
      );

      const receiverTransfer = await sspLightningService.providePreimage(
        preimage
      );

      expect(receiverTransfer.status).toEqual(
        TransferStatus.TRANSFER_STATUS_SENDER_KEY_TWEAKED
      );
      expect(receiverTransfer.id).toEqual(transfer.id);

      const leafPrivKeyMap = await sspTransferService.verifyPendingTransfer(
        receiverTransfer
      );

      expect(leafPrivKeyMap.size).toBe(1);
      expect(leafPrivKeyMap.has(nodeToSend.id)).toBe(true);
      expect(
        equalBytes(leafPrivKeyMap.get(nodeToSend.id)!, newLeafPubKey)
      ).toBe(true);

      const finalLeafPubKey = await sspWallet
        .getSigner()
        .generatePublicKey(sha256("2"));

      expect(receiverTransfer.leaves[0].leaf).toBeDefined();

      const claimingNode = {
        leaf: receiverTransfer.leaves[0].leaf!,
        signingPubKey: newLeafPubKey,
        newSigningPubKey: finalLeafPubKey,
      };

      await sspTransferService.claimTransfer(receiverTransfer, [claimingNode]);
    },
    60000
  );
});
