import { hexToBytes } from "@noble/curves/utils";
import { Transaction } from "@scure/btc-signer";
import { TransactionInput } from "@scure/btc-signer/psbt";
import { ValidationError } from "../errors/types.js";
import { SigningCommitment } from "../proto/common.js";
import {
  RequestedSigningCommitments,
  UserSignedTxSigningJob,
} from "../proto/spark.js";
import {
  getSigHashFromTx,
  getTxFromRawTxBytes,
  getTxId,
} from "../utils/bitcoin.js";
import {
  createRefundTxs,
  getNextTransactionSequence,
} from "../utils/transaction.js";
import { WalletConfigService } from "./config.js";
import type { LeafKeyTweak } from "./transfer.js";

export class SigningService {
  private readonly config: WalletConfigService;

  constructor(config: WalletConfigService) {
    this.config = config;
  }

  private async signRefundsInternal(
    refundTx: Transaction,
    sighash: Uint8Array,
    leaf: LeafKeyTweak,
    signingCommitments:
      | {
          [key: string]: SigningCommitment;
        }
      | undefined,
  ): Promise<UserSignedTxSigningJob[]> {
    const leafSigningJobs: UserSignedTxSigningJob[] = [];

    const signingCommitment =
      await this.config.signer.getRandomSigningCommitment();

    if (!signingCommitments) {
      throw new ValidationError("Invalid signing commitments", {
        field: "signingNonceCommitments",
        value: signingCommitments,
        expected: "Non-null signing commitments",
      });
    }
    const signingResult = await this.config.signer.signFrost({
      message: sighash,
      keyDerivation: leaf.keyDerivation,
      publicKey: await this.config.signer.getPublicKeyFromDerivation(
        leaf.keyDerivation,
      ),
      selfCommitment: signingCommitment,
      statechainCommitments: signingCommitments,
      adaptorPubKey: new Uint8Array(),
      verifyingKey: leaf.leaf.verifyingPublicKey,
    });

    leafSigningJobs.push({
      leafId: leaf.leaf.id,
      signingPublicKey: await this.config.signer.getPublicKeyFromDerivation(
        leaf.keyDerivation,
      ),
      rawTx: refundTx.toBytes(),
      signingNonceCommitment: signingCommitment.commitment,
      userSignature: signingResult,
      signingCommitments: {
        signingCommitments: signingCommitments,
      },
    });

    return leafSigningJobs;
  }

  async signRefunds(
    leaves: LeafKeyTweak[],
    receiverIdentityPubkey: Uint8Array,
    cpfpSigningCommitments: RequestedSigningCommitments[],
    directSigningCommitments: RequestedSigningCommitments[],
    directFromCpfpSigningCommitments: RequestedSigningCommitments[],
  ): Promise<{
    cpfpLeafSigningJobs: UserSignedTxSigningJob[];
    directLeafSigningJobs: UserSignedTxSigningJob[];
    directFromCpfpLeafSigningJobs: UserSignedTxSigningJob[];
  }> {
    const cpfpLeafSigningJobs: UserSignedTxSigningJob[] = [];
    const directLeafSigningJobs: UserSignedTxSigningJob[] = [];
    const directFromCpfpLeafSigningJobs: UserSignedTxSigningJob[] = [];

    for (let i = 0; i < leaves.length; i++) {
      const leaf = leaves[i];
      if (!leaf?.leaf) {
        throw new ValidationError("Leaf not found in signRefunds", {
          field: "leaf",
          value: leaf,
          expected: "Non-null leaf",
        });
      }

      const nodeTx = getTxFromRawTxBytes(leaf.leaf.nodeTx);
      const cpfpNodeOutPoint: TransactionInput = {
        txid: hexToBytes(getTxId(nodeTx)),
        index: 0,
      };

      const currRefundTx = getTxFromRawTxBytes(leaf.leaf.refundTx);

      const sequence = currRefundTx.getInput(0).sequence;
      if (!sequence) {
        throw new ValidationError("Invalid refund transaction", {
          field: "sequence",
          value: currRefundTx.getInput(0),
          expected: "Non-null sequence",
        });
      }
      const { nextSequence, nextDirectSequence } =
        getNextTransactionSequence(sequence);

      const amountSats = currRefundTx.getOutput(0).amount;
      if (amountSats === undefined) {
        throw new ValidationError("Invalid refund transaction", {
          field: "amount",
          value: currRefundTx.getOutput(0),
          expected: "Non-null amount",
        });
      }

      let directNodeTx: Transaction | undefined;
      let directNodeOutPoint: TransactionInput | undefined;
      if (leaf.leaf.directTx.length > 0) {
        directNodeTx = getTxFromRawTxBytes(leaf.leaf.directTx);
        directNodeOutPoint = {
          txid: hexToBytes(getTxId(directNodeTx)),
          index: 0,
        };
      }

      const { cpfpRefundTx, directRefundTx, directFromCpfpRefundTx } =
        createRefundTxs({
          sequence: nextSequence,
          directSequence: nextDirectSequence,
          input: cpfpNodeOutPoint,
          directInput: directNodeOutPoint,
          amountSats,
          receivingPubkey: receiverIdentityPubkey,
          network: this.config.getNetwork(),
        });

      const refundSighash = getSigHashFromTx(
        cpfpRefundTx,
        0,
        nodeTx.getOutput(0),
      );
      const signingJobs = await this.signRefundsInternal(
        cpfpRefundTx,
        refundSighash,
        leaf,
        cpfpSigningCommitments[i]?.signingNonceCommitments,
      );

      cpfpLeafSigningJobs.push(...signingJobs);

      if (directRefundTx) {
        if (!directNodeTx) {
          throw new ValidationError(
            "Direct node transaction undefined while direct refund transaction is defined",
            {
              field: "directNodeTx",
              value: directNodeTx,
              expected: "Non-null direct node transaction",
            },
          );
        }
        const refundSighash = getSigHashFromTx(
          directRefundTx,
          0,
          directNodeTx.getOutput(0),
        );
        const signingJobs = await this.signRefundsInternal(
          directRefundTx,
          refundSighash,
          leaf,
          directSigningCommitments[i]?.signingNonceCommitments,
        );
        directLeafSigningJobs.push(...signingJobs);
      }

      if (directFromCpfpRefundTx) {
        if (!directNodeTx) {
          throw new ValidationError(
            "Direct node transaction undefined while direct from CPFP refund transaction is defined",
            {
              field: "directNodeTx",
              value: directNodeTx,
              expected: "Non-null direct node transaction",
            },
          );
        }
        const refundSighash = getSigHashFromTx(
          directFromCpfpRefundTx,
          0,
          nodeTx.getOutput(0),
        );
        const signingJobs = await this.signRefundsInternal(
          directFromCpfpRefundTx,
          refundSighash,
          leaf,
          directFromCpfpSigningCommitments[i]?.signingNonceCommitments,
        );
        directFromCpfpLeafSigningJobs.push(...signingJobs);
      }
    }

    return {
      cpfpLeafSigningJobs,
      directLeafSigningJobs,
      directFromCpfpLeafSigningJobs,
    };
  }
}
