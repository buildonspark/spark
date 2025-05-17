import { WalletConfigService } from "@buildonspark/spark-sdk/config";
import { ConnectionManager } from "@buildonspark/spark-sdk/connection";
import {
  FreezeTokensPayload,
  FreezeTokensResponse,
} from "@buildonspark/spark-sdk/proto/spark";
import { collectResponses } from "@buildonspark/spark-sdk/utils";
import { hashFreezeTokensPayload } from "../utils/token-hashing.js";
import { NetworkError } from "@buildonspark/spark-sdk";
import { hexToBytes } from "@noble/curves/abstract/utils";

export class TokenFreezeService {
  private readonly config: WalletConfigService;
  private readonly connectionManager: ConnectionManager;

  constructor(
    config: WalletConfigService,
    connectionManager: ConnectionManager,
  ) {
    this.config = config;
    this.connectionManager = connectionManager;
  }

  async freezeTokens(
    ownerPublicKey: Uint8Array,
    tokenPublicKey: Uint8Array,
  ): Promise<FreezeTokensResponse> {
    return this.freezeOperation(ownerPublicKey, tokenPublicKey, false);
  }

  async unfreezeTokens(
    ownerPublicKey: Uint8Array,
    tokenPublicKey: Uint8Array,
  ): Promise<FreezeTokensResponse> {
    return this.freezeOperation(ownerPublicKey, tokenPublicKey, true);
  }

  private async freezeOperation(
    ownerPublicKey: Uint8Array,
    tokenPublicKey: Uint8Array,
    shouldUnfreeze: boolean,
  ): Promise<FreezeTokensResponse> {
    const signingOperators = this.config.getSigningOperators();
    const issuerProvidedTimestamp = Date.now();

    // Submit freeze_tokens to all SOs in parallel
    const freezeResponses = await Promise.allSettled(
      Object.entries(signingOperators).map(async ([identifier, operator]) => {
        const internalSparkClient =
          await this.connectionManager.createSparkClient(operator.address);

        const freezeTokensPayload: FreezeTokensPayload = {
          ownerPublicKey,
          tokenPublicKey,
          shouldUnfreeze,
          issuerProvidedTimestamp,
          operatorIdentityPublicKey: hexToBytes(operator.identityPublicKey),
        };

        const hashedPayload: Uint8Array =
          hashFreezeTokensPayload(freezeTokensPayload);

        const issuerSignature =
          await this.config.signer.signMessageWithIdentityKey(hashedPayload);

        try {
          const response = await internalSparkClient.freeze_tokens({
            freezeTokensPayload,
            issuerSignature,
          });

          return {
            identifier,
            response,
          };
        } catch (error) {
          throw new NetworkError(
            `Failed to send a freeze/unfreeze operation for token: ${tokenPublicKey.toString()} at operator: ${operator.address}`,
            {
              operation: "freeze_tokens",
              errorCount: 1,
              errors: error instanceof Error ? error.message : String(error),
            },
            error instanceof Error ? error : undefined,
          );
        }
      }),
    );

    const successfulResponses = collectResponses(freezeResponses);

    return successfulResponses[0].response;
  }
}
