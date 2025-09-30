import { Transaction } from "@scure/btc-signer";
import { ValidationError } from "../errors/types.js";
import { SigningCommitment } from "../proto/common.js";
import {
  RequestedSigningCommitments,
  UserSignedTxSigningJob,
} from "../proto/spark.js";
import { getSigHashFromTx, getTxFromRawTxBytes } from "../utils/bitcoin.js";
import { createRefundTxsForLightning } from "../utils/htlc-transactions.js";
import { getNetwork } from "../utils/network.js";
import {
  createDecrementedTimelockRefundTxs,
  getCurrentTimelock,
  getNextHTLCTransactionSequence,
} from "../utils/transaction.js";
import { WalletConfigService } from "./config.js";
import type {
  LeafKeyTweak,
  SigningJobType,
  SigningJobWithOptionalNonce,
} from "./transfer.js";

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

      const currRefundTx = getTxFromRawTxBytes(leaf.leaf.refundTx);

      const amountSats = currRefundTx.getOutput(0).amount;
      if (amountSats === undefined) {
        throw new ValidationError("Invalid refund transaction", {
          field: "amount",
          value: currRefundTx.getOutput(0),
          expected: "Non-null amount",
        });
      }

      let directNodeTx: Transaction | undefined;
      if (leaf.leaf.directTx.length > 0) {
        directNodeTx = getTxFromRawTxBytes(leaf.leaf.directTx);
      }

      const currentSequence = currRefundTx.getInput(0).sequence;
      if (!currentSequence) {
        throw new ValidationError("Invalid refund transaction", {
          field: "sequence",
          value: currRefundTx.getInput(0),
          expected: "Non-null sequence",
        });
      }

      const { cpfpRefundTx, directRefundTx, directFromCpfpRefundTx } =
        createDecrementedTimelockRefundTxs({
          nodeTx: nodeTx,
          directNodeTx: directNodeTx,
          sequence: currentSequence,
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

      const isZeroNode = getCurrentTimelock(nodeTx.getInput(0).sequence);
      if (directRefundTx && !isZeroNode) {
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

  async signRefundsForLightning(
    leaves: LeafKeyTweak[],
    receiverIdentityPubkey: Uint8Array,
    cpfpSigningCommitments: RequestedSigningCommitments[],
    directSigningCommitments: RequestedSigningCommitments[],
    directFromCpfpSigningCommitments: RequestedSigningCommitments[],
    hash: Uint8Array,
  ): Promise<{
    cpfpLeafSigningJobs: UserSignedTxSigningJob[];
    directLeafSigningJobs: UserSignedTxSigningJob[];
    directFromCpfpLeafSigningJobs: UserSignedTxSigningJob[];
  }> {
    const network = getNetwork(this.config.getNetwork());
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

      const currRefundTx = getTxFromRawTxBytes(leaf.leaf.refundTx);

      const sequence = currRefundTx.getInput(0).sequence;
      if (!sequence) {
        throw new ValidationError("Invalid refund transaction", {
          field: "sequence",
          value: currRefundTx.getInput(0),
          expected: "Non-null sequence",
        });
      }

      const amountSats = currRefundTx.getOutput(0).amount;
      if (amountSats === undefined) {
        throw new ValidationError("Invalid refund transaction", {
          field: "amount",
          value: currRefundTx.getOutput(0),
          expected: "Non-null amount",
        });
      }

      const { nextSequence, nextDirectSequence } =
        getNextHTLCTransactionSequence(sequence);

      let directNodeTx: Transaction | undefined;
      if (leaf.leaf.directTx.length > 0) {
        directNodeTx = getTxFromRawTxBytes(leaf.leaf.directTx);
      }

      const identityPublicKey = await this.config.signer.getIdentityPublicKey();

      const { cpfpRefundTx, directRefundTx, directFromCpfpRefundTx } =
        createRefundTxsForLightning({
          nodeTx: nodeTx,
          directNodeTx: directNodeTx,
          vout: 0,
          network,
          sequence: nextSequence,
          directSequence: nextDirectSequence,
          hash,
          hashLockDestinationPubkey: receiverIdentityPubkey,
          sequenceLockDestinationPubkey: identityPublicKey,
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

  async signSigningJobs(
    signingJobs: (SigningJobWithOptionalNonce & RequestedSigningCommitments)[],
  ): Promise<Map<SigningJobType, UserSignedTxSigningJob>> {
    const userSignedTxSigningJobs: Map<SigningJobType, UserSignedTxSigningJob> =
      new Map();

    for (const signingJob of signingJobs) {
      const rawTx = getTxFromRawTxBytes(signingJob.rawTx);
      const txOut = signingJob.parentTxOut;
      const rawTxSighash = getSigHashFromTx(rawTx, 0, txOut);
      const userSignature = await this.config.signer.signFrost({
        message: rawTxSighash,
        keyDerivation: signingJob.keyDerivation,
        publicKey: signingJob.signingPublicKey,
        verifyingKey: signingJob.verifyingKey,
        selfCommitment: signingJob.signingNonceCommitment,
        statechainCommitments: signingJob.signingNonceCommitments,
        adaptorPubKey: new Uint8Array(),
      });

      const userSignedTxSigningJob: UserSignedTxSigningJob = {
        leafId: signingJob.leafId,
        signingPublicKey: signingJob.signingPublicKey,
        rawTx: rawTx.toBytes(),
        signingNonceCommitment: signingJob.signingNonceCommitment.commitment,
        signingCommitments: {
          signingCommitments: signingJob.signingNonceCommitments,
        },
        userSignature,
      };

      userSignedTxSigningJobs.set(signingJob.type, userSignedTxSigningJob);
    }

    return userSignedTxSigningJobs;
  }
}
