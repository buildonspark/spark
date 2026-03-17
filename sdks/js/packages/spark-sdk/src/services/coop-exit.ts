import { hexToBytes } from "@noble/curves/utils";
import { TransactionInput } from "@scure/btc-signer/psbt";
import { uuidv7 } from "uuidv7";
import { SparkRequestError, SparkValidationError } from "../errors/types.js";
import {
  CooperativeExitResponse,
  HashVariant,
  SendLeafKeyTweaks,
  Transfer,
} from "../proto/spark.js";
import { getSparkFrost } from "../spark-bindings/spark-bindings.js";
import { Network } from "../utils/network.js";
import { getTransferPackageSigningPayload } from "../utils/transfer_package.js";
import { WalletConfigService } from "./config.js";
import { ConnectionManager } from "./connection/connection.js";
import { SigningService } from "./signing.js";
import type { LeafKeyTweak } from "./transfer.js";
import {
  BaseTransferService,
  type TransferPackageWithSelfCommitments,
} from "./transfer.js";

export type GetConnectorRefundSignaturesParams = {
  leaves: LeafKeyTweak[];
  exitTxId: Uint8Array;
  connectorOutputs: TransactionInput[];
  receiverPubKey: Uint8Array;
  transferId: string;
  connectorTx: Uint8Array;
};

export class CoopExitService extends BaseTransferService {
  constructor(
    config: WalletConfigService,
    connectionManager: ConnectionManager,
    signingService: SigningService,
  ) {
    super(config, connectionManager, signingService);
  }

  async getConnectorRefundSignatures({
    leaves,
    exitTxId,
    connectorOutputs,
    receiverPubKey,
    transferId,
    connectorTx,
  }: GetConnectorRefundSignaturesParams): Promise<{
    transfer: Transfer;
  }> {
    if (leaves.length !== connectorOutputs.length) {
      throw new SparkValidationError(
        "Mismatch between leaves and connector outputs",
        {
          field: "leaves/connectorOutputs",
          value: {
            leavesCount: leaves.length,
            outputsCount: connectorOutputs.length,
          },
          expected: "Equal length",
        },
      );
    }

    // 1. Prepare key tweaks (empty signature maps since SO hasn't signed yet)
    const keyTweakInputMap = await this.prepareSendTransferKeyTweaks(
      transferId,
      leaves,
    );

    // 2. Get SO signing commitments (3 per leaf: cpfp, direct, directFromCpfp)
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    const nodeIds = leaves.map((leaf) => leaf.leaf.id);
    const signingCommitments = await sparkClient.get_signing_commitments({
      nodeIds,
      count: 3,
    });

    // 3. Sign refunds with connector inputs
    const {
      cpfpLeafSigningJobs,
      directLeafSigningJobs,
      directFromCpfpLeafSigningJobs,
    } = await this.signingService.signRefundsForCoopExit(
      leaves,
      connectorOutputs,
      connectorTx,
      signingCommitments.signingCommitments.slice(0, leaves.length),
      signingCommitments.signingCommitments.slice(
        leaves.length,
        2 * leaves.length,
      ),
      signingCommitments.signingCommitments.slice(2 * leaves.length),
    );

    // 4. Build TransferPackage with encrypted key tweaks
    const sparkFrost = getSparkFrost();
    const encryptedKeyTweaksEntries = await Promise.all(
      Array.from(keyTweakInputMap.entries()).map(async ([key, value]) => {
        const protoToEncrypt: SendLeafKeyTweaks = {
          leavesToSend: value,
        };

        const protoToEncryptBinary =
          SendLeafKeyTweaks.encode(protoToEncrypt).finish();

        const operator = this.config.getSigningOperators()[key];
        if (!operator) {
          throw new SparkValidationError("Operator not found");
        }

        const encryptedProto = await sparkFrost.encryptEcies(
          protoToEncryptBinary,
          hexToBytes(operator.identityPublicKey),
        );

        return [key, Uint8Array.from(encryptedProto)] as const;
      }),
    );
    const encryptedKeyTweaks = Object.fromEntries(encryptedKeyTweaksEntries);

    const transferPackage: TransferPackageWithSelfCommitments = {
      leavesToSend: cpfpLeafSigningJobs,
      keyTweakPackage: encryptedKeyTweaks,
      userSignature: new Uint8Array(),
      directLeavesToSend: directLeafSigningJobs,
      directFromCpfpLeavesToSend: directFromCpfpLeafSigningJobs,
      hashVariant: HashVariant.HASH_VARIANT_V2,
    };

    const transferPackageSigningPayload = getTransferPackageSigningPayload(
      transferId,
      transferPackage,
    );
    const signature = await this.config.signer.signMessageWithIdentityKey(
      transferPackageSigningPayload,
    );
    transferPackage.userSignature = new Uint8Array(signature);

    // 5. Call cooperative_exit_v2 with TransferPackage
    let response: CooperativeExitResponse;
    try {
      response = await sparkClient.cooperative_exit_v2({
        transfer: {
          transferId,
          ownerIdentityPublicKey:
            await this.config.signer.getIdentityPublicKey(),
          receiverIdentityPublicKey: receiverPubKey,
          transferPackage,
          expiryTime:
            this.config.getNetwork() == Network.MAINNET
              ? new Date(Date.now() + 7 * 24 * 60 * 60 * 1000 + 5 * 60 * 1000)
              : new Date(Date.now() + 35 * 60 * 1000),
        },
        exitId: uuidv7(),
        exitTxid: exitTxId,
        connectorTx: connectorTx,
      });
    } catch (error) {
      throw new SparkRequestError("Failed to initiate cooperative exit", {
        operation: "cooperative_exit_v2",
        error,
      });
    }

    if (!response.transfer) {
      throw new SparkRequestError("Failed to initiate cooperative exit", {
        operation: "cooperative_exit_v2",
        error: new Error("No transfer in response"),
      });
    }

    return { transfer: response.transfer };
  }
}
