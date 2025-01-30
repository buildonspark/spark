import { secp256k1 } from "@noble/curves/secp256k1";
import { WalletConfigService } from "./config";
import { ConnectionManager } from "./connection";
import { sha256 } from "@scure/btc-signer/utils";
import { bytesToNumberBE, numberToBytesBE } from "@noble/curves/abstract/utils";
import { splitSecretWithProofs } from "../utils/secret-sharing";

export type CreateLightningInvoiceParams = {
  invoiceCreator: (
    amountSats: number,
    paymentHash: Uint8Array,
    memo: string
  ) => Promise<string>;
  amountSats: number;
  memo: string;
};

export class LightningService {
  private readonly config: WalletConfigService;
  private readonly connectionManager: ConnectionManager;

  constructor(
    config: WalletConfigService,
    connectionManager: ConnectionManager
  ) {
    this.config = config;
    this.connectionManager = connectionManager;
  }

  async createLightningInvoice({
    invoiceCreator,
    amountSats,
    memo,
  }: CreateLightningInvoiceParams): Promise<string> {
    const preimagePrivKey = secp256k1.utils.randomPrivateKey();
    const paymentHash = sha256(preimagePrivKey);

    const invoice = await invoiceCreator(amountSats, paymentHash, memo);
    const preimageAsInt = bytesToNumberBE(preimagePrivKey);

    const shares = splitSecretWithProofs(
      preimageAsInt,
      secp256k1.CURVE.n,
      this.config.getConfig().threshold,
      Object.keys(this.config.getConfig().signingOperators).length
    );

    const errors: Error[] = [];
    const promises = Object.entries(
      this.config.getConfig().signingOperators
    ).map(async ([_, operator]) => {
      const share = shares[operator.id];

      const sparkClient = await this.connectionManager.createSparkClient(
        operator.address,
        this.config
      );

      try {
        await sparkClient.store_preimage_share({
          paymentHash: paymentHash,
          preimageShare: {
            secretShare: numberToBytesBE(share.share, 32),
            proofs: share.proofs,
          },
          threshold: this.config.getConfig().threshold,
          invoiceString: invoice,
          userIdentityPublicKey: this.config.getIdentityPublicKey(),
        });
      } catch (e: any) {
        errors.push(e);
      } finally {
        sparkClient.close?.();
      }
    });

    await Promise.all(promises);

    if (errors.length > 0) {
      throw new Error(errors.map((e) => e.message).join("\n"));
    }

    return invoice;
  }
}
