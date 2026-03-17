import { Transaction } from "@scure/btc-signer";
import { SparkValidationError } from "../errors/types.js";
import { SigningCommitment } from "../proto/common.js";
import {
  RequestedSigningCommitments,
  UserSignedTxSigningJob,
} from "../proto/spark.js";
import { SigningCommitmentWithOptionalNonce } from "../signer/types.js";
import {
  getSigHashFromMultiInputTx,
  getSigHashFromTx,
  getTxFromRawTxBytes,
} from "../utils/bitcoin.js";
import { TransactionInput } from "@scure/btc-signer/psbt";
import { createRefundTxsForLightning } from "../utils/htlc-transactions.js";
import { getNetwork } from "../utils/network.js";
import {
  createConnectorRefundTxs,
  createCurrentTimelockRefundTxs,
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

export type UserSignedTxSigningJobWithSelfCommitment =
  UserSignedTxSigningJob & {
    selfCommitment: SigningCommitmentWithOptionalNonce;
  };
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
    adaptorPubKey: Uint8Array = new Uint8Array(),
  ): Promise<UserSignedTxSigningJobWithSelfCommitment[]> {
    const leafSigningJobs: UserSignedTxSigningJobWithSelfCommitment[] = [];

    const signingCommitment =
      await this.config.signer.getRandomSigningCommitment();

    if (!signingCommitments) {
      throw new SparkValidationError("Invalid signing commitments", {
        field: "signingNonceCommitments",
        value: signingCommitments,
        expected: "Non-null signing commitments",
      });
    }
    const publicKey = await this.config.signer.getPublicKeyFromDerivation(
      leaf.keyDerivation,
    );
    const signingResult = await this.config.signer.signFrost({
      message: sighash,
      keyDerivation: leaf.keyDerivation,
      publicKey,
      selfCommitment: signingCommitment,
      statechainCommitments: signingCommitments,
      adaptorPubKey,
      verifyingKey: leaf.leaf.verifyingPublicKey,
    });

    leafSigningJobs.push({
      leafId: leaf.leaf.id,
      signingPublicKey: publicKey,
      rawTx: refundTx.toBytes(),
      signingNonceCommitment: signingCommitment.commitment,
      userSignature: signingResult,
      signingCommitments: {
        signingCommitments: signingCommitments,
      },
      additionalInputs: [],
      selfCommitment: signingCommitment,
    });

    return leafSigningJobs;
  }

  private async signRefundsCore(
    leaves: LeafKeyTweak[],
    createRefundTxs: typeof createDecrementedTimelockRefundTxs,
    cpfpSigningCommitments: RequestedSigningCommitments[],
    directSigningCommitments: RequestedSigningCommitments[],
    directFromCpfpSigningCommitments: RequestedSigningCommitments[],
    adaptorPubKey?: Uint8Array,
  ): Promise<{
    cpfpLeafSigningJobs: UserSignedTxSigningJobWithSelfCommitment[];
    directLeafSigningJobs: UserSignedTxSigningJobWithSelfCommitment[];
    directFromCpfpLeafSigningJobs: UserSignedTxSigningJobWithSelfCommitment[];
  }> {
    const cpfpLeafSigningJobs: UserSignedTxSigningJobWithSelfCommitment[] = [];
    const directLeafSigningJobs: UserSignedTxSigningJobWithSelfCommitment[] =
      [];
    const directFromCpfpLeafSigningJobs: UserSignedTxSigningJobWithSelfCommitment[] =
      [];

    for (let i = 0; i < leaves.length; i++) {
      const leaf = leaves[i];
      if (!leaf?.leaf) {
        throw new SparkValidationError("Leaf not found in signRefundsCore", {
          field: "leaf",
          value: leaf,
          expected: "Non-null leaf",
        });
      }

      const receivingPubkey = leaf.receiverIdentityPublicKey;
      const nodeTx = getTxFromRawTxBytes(leaf.leaf.nodeTx);
      const currRefundTx = getTxFromRawTxBytes(leaf.leaf.refundTx);

      const amountSats = currRefundTx.getOutput(0).amount;
      if (amountSats === undefined) {
        throw new SparkValidationError("Invalid refund transaction", {
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
      if (currentSequence == null) {
        throw new SparkValidationError("Invalid refund transaction", {
          field: "sequence",
          value: currRefundTx.getInput(0),
          expected: "Non-null sequence",
        });
      }

      const { cpfpRefundTx, directRefundTx, directFromCpfpRefundTx } =
        await createRefundTxs({
          nodeTx,
          directNodeTx,
          sequence: currentSequence,
          receivingPubkey,
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
        adaptorPubKey,
      );
      cpfpLeafSigningJobs.push(...signingJobs);

      const isZeroNode = !getCurrentTimelock(nodeTx.getInput(0).sequence);
      if (directRefundTx && !isZeroNode) {
        if (!directNodeTx) {
          throw new SparkValidationError(
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
          adaptorPubKey,
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
          adaptorPubKey,
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

  async signRefunds(
    leaves: LeafKeyTweak[],
    cpfpSigningCommitments: RequestedSigningCommitments[],
    directSigningCommitments: RequestedSigningCommitments[],
    directFromCpfpSigningCommitments: RequestedSigningCommitments[],
    adaptorPubKey?: Uint8Array,
  ): Promise<{
    cpfpLeafSigningJobs: UserSignedTxSigningJobWithSelfCommitment[];
    directLeafSigningJobs: UserSignedTxSigningJobWithSelfCommitment[];
    directFromCpfpLeafSigningJobs: UserSignedTxSigningJobWithSelfCommitment[];
  }> {
    return this.signRefundsCore(
      leaves,
      createDecrementedTimelockRefundTxs,
      cpfpSigningCommitments,
      directSigningCommitments,
      directFromCpfpSigningCommitments,
      adaptorPubKey,
    );
  }

  async signRefundsForClaim(
    leaves: LeafKeyTweak[],
    cpfpSigningCommitments: RequestedSigningCommitments[],
    directSigningCommitments: RequestedSigningCommitments[],
    directFromCpfpSigningCommitments: RequestedSigningCommitments[],
  ): Promise<{
    cpfpLeafSigningJobs: UserSignedTxSigningJobWithSelfCommitment[];
    directLeafSigningJobs: UserSignedTxSigningJobWithSelfCommitment[];
    directFromCpfpLeafSigningJobs: UserSignedTxSigningJobWithSelfCommitment[];
  }> {
    return this.signRefundsCore(
      leaves,
      createCurrentTimelockRefundTxs,
      cpfpSigningCommitments,
      directSigningCommitments,
      directFromCpfpSigningCommitments,
    );
  }

  async signRefundsForCoopExit(
    leaves: LeafKeyTweak[],
    connectorOutputs: TransactionInput[],
    connectorTx: Uint8Array,
    cpfpSigningCommitments: RequestedSigningCommitments[],
    directSigningCommitments: RequestedSigningCommitments[],
    directFromCpfpSigningCommitments: RequestedSigningCommitments[],
  ): Promise<{
    cpfpLeafSigningJobs: UserSignedTxSigningJobWithSelfCommitment[];
    directLeafSigningJobs: UserSignedTxSigningJobWithSelfCommitment[];
    directFromCpfpLeafSigningJobs: UserSignedTxSigningJobWithSelfCommitment[];
  }> {
    const cpfpLeafSigningJobs: UserSignedTxSigningJobWithSelfCommitment[] = [];
    const directLeafSigningJobs: UserSignedTxSigningJobWithSelfCommitment[] =
      [];
    const directFromCpfpLeafSigningJobs: UserSignedTxSigningJobWithSelfCommitment[] =
      [];

    const connectorTxParsed = getTxFromRawTxBytes(connectorTx);

    for (let i = 0; i < leaves.length; i++) {
      const leaf = leaves[i];
      if (!leaf?.leaf) {
        throw new SparkValidationError(
          "Leaf not found in signRefundsForCoopExit",
          {
            field: "leaf",
            value: leaf,
            expected: "Non-null leaf",
          },
        );
      }

      const connectorOutput = connectorOutputs[i];
      if (!connectorOutput || connectorOutput.index === undefined) {
        throw new SparkValidationError("Missing connector output", {
          field: "connectorOutput",
          value: connectorOutput,
          expected: "Valid connector output with index",
        });
      }

      const connectorPrevOutput = connectorTxParsed.getOutput(
        connectorOutput.index,
      );
      if (
        !connectorPrevOutput ||
        !connectorPrevOutput.script ||
        connectorPrevOutput.amount === undefined
      ) {
        throw new SparkValidationError("Invalid connector transaction output", {
          field: "connectorPrevOutput",
          value: connectorPrevOutput,
          expected: "Valid output with script and amount",
        });
      }

      const nodeTx = getTxFromRawTxBytes(leaf.leaf.nodeTx);
      const nodeTxOutput = nodeTx.getOutput(0);

      const currRefundTx = getTxFromRawTxBytes(leaf.leaf.refundTx);
      const currentSequence = currRefundTx.getInput(0).sequence;
      if (!currentSequence) {
        throw new SparkValidationError("Invalid refund transaction", {
          field: "sequence",
          value: currRefundTx.getInput(0),
          expected: "Non-null sequence",
        });
      }

      const isZeroNode = !getCurrentTimelock(nodeTx.getInput(0).sequence);

      let directNodeTx: Transaction | undefined;
      if (leaf.leaf.directTx.length > 0 && !isZeroNode) {
        directNodeTx = getTxFromRawTxBytes(leaf.leaf.directTx);
      }

      const { cpfpRefundTx, directRefundTx, directFromCpfpRefundTx } =
        await createConnectorRefundTxs({
          nodeTx,
          directNodeTx,
          sequence: currentSequence,
          connectorOutput,
          receivingPubkey: leaf.receiverIdentityPublicKey,
          network: this.config.getNetwork(),
        });

      // CPFP refund: sign input 0 with multi-input sighash
      const cpfpSighash = getSigHashFromMultiInputTx(cpfpRefundTx, 0, [
        nodeTxOutput,
        connectorPrevOutput,
      ]);
      const cpfpJobs = await this.signRefundsInternal(
        cpfpRefundTx,
        cpfpSighash,
        leaf,
        cpfpSigningCommitments[i]?.signingNonceCommitments,
      );
      cpfpLeafSigningJobs.push(...cpfpJobs);

      // Direct refund (spends direct tx output)
      if (directRefundTx && !isZeroNode) {
        if (!directNodeTx) {
          throw new SparkValidationError(
            "Direct node transaction undefined while direct refund transaction is defined",
            {
              field: "directNodeTx",
              value: directNodeTx,
              expected: "Non-null direct node transaction",
            },
          );
        }
        const directTxOutput = directNodeTx.getOutput(0);
        const directSighash = getSigHashFromMultiInputTx(directRefundTx, 0, [
          directTxOutput,
          connectorPrevOutput,
        ]);
        const directJobs = await this.signRefundsInternal(
          directRefundTx,
          directSighash,
          leaf,
          directSigningCommitments[i]?.signingNonceCommitments,
        );
        directLeafSigningJobs.push(...directJobs);
      }

      // Direct-from-CPFP refund (spends CPFP node tx output)
      if (directFromCpfpRefundTx) {
        const directFromCpfpSighash = getSigHashFromMultiInputTx(
          directFromCpfpRefundTx,
          0,
          [nodeTxOutput, connectorPrevOutput],
        );
        const directFromCpfpJobs = await this.signRefundsInternal(
          directFromCpfpRefundTx,
          directFromCpfpSighash,
          leaf,
          directFromCpfpSigningCommitments[i]?.signingNonceCommitments,
        );
        directFromCpfpLeafSigningJobs.push(...directFromCpfpJobs);
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
        throw new SparkValidationError("Leaf not found in signRefunds", {
          field: "leaf",
          value: leaf,
          expected: "Non-null leaf",
        });
      }

      const nodeTx = getTxFromRawTxBytes(leaf.leaf.nodeTx);

      const currRefundTx = getTxFromRawTxBytes(leaf.leaf.refundTx);

      const sequence = currRefundTx.getInput(0).sequence;
      if (sequence == null) {
        throw new SparkValidationError("Invalid refund transaction", {
          field: "sequence",
          value: currRefundTx.getInput(0),
          expected: "Non-null sequence",
        });
      }

      const amountSats = currRefundTx.getOutput(0).amount;
      if (amountSats === undefined) {
        throw new SparkValidationError("Invalid refund transaction", {
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
          hashLockDestinationPubkey: leaf.receiverIdentityPublicKey,
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
        undefined,
      );

      cpfpLeafSigningJobs.push(...signingJobs);

      if (directRefundTx) {
        if (!directNodeTx) {
          throw new SparkValidationError(
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
          undefined,
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
          undefined,
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
        additionalInputs: [],
      };

      userSignedTxSigningJobs.set(signingJob.type, userSignedTxSigningJob);
    }

    return userSignedTxSigningJobs;
  }
}
