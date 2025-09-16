import {
  type BaseConnectionManager,
  NetworkError,
  WalletConfigService,
  collectResponses,
} from "@buildonspark/spark-sdk";
import {
  FreezeTokensPayload,
  FreezeTokensResponse,
} from "@buildonspark/spark-sdk/proto/spark_token";
import { hexToBytes } from "@noble/curves/utils";
import { hashFreezeTokensPayload } from "../utils/token-hashing.js";

export class TokenFreezeService {
  private readonly config: WalletConfigService;
  private readonly connectionManager: BaseConnectionManager;

  constructor(
    config: WalletConfigService,
    connectionManager: BaseConnectionManager,
  ) {
    this.config = config;
    this.connectionManager = connectionManager;
  }

  async freezeTokens({
    ownerPublicKey,
    tokenIdentifier,
  }: {
    ownerPublicKey: Uint8Array;
    tokenIdentifier?: Uint8Array;
  }): Promise<FreezeTokensResponse> {
    return this.freezeOperation(ownerPublicKey, false, tokenIdentifier!);
  }

  async unfreezeTokens({
    ownerPublicKey,
    tokenIdentifier,
  }: {
    ownerPublicKey: Uint8Array;
    tokenIdentifier?: Uint8Array;
  }): Promise<FreezeTokensResponse> {
    return this.freezeOperation(ownerPublicKey, true, tokenIdentifier!);
  }

  private async freezeOperation(
    ownerPublicKey: Uint8Array,
    shouldUnfreeze: boolean,
    tokenIdentifier: Uint8Array,
  ): Promise<FreezeTokensResponse> {
    const signingOperators = this.config.getSigningOperators();
    const issuerProvidedTimestamp = Date.now();

    // Submit freeze_tokens to all SOs in parallel
    const freezeResponses = await Promise.allSettled(
      Object.entries(signingOperators).map(async ([identifier, operator]) => {
        const sparkTokenClient =
          await this.connectionManager.createSparkTokenClient(operator.address);

        const freezeTokensPayload: FreezeTokensPayload = {
          version: 1,
          ownerPublicKey,
          tokenIdentifier,
          shouldUnfreeze,
          issuerProvidedTimestamp,
          operatorIdentityPublicKey: hexToBytes(operator.identityPublicKey),
        };

        const hashedPayload: Uint8Array =
          hashFreezeTokensPayload(freezeTokensPayload);

        const issuerSignature =
          await this.config.signer.signMessageWithIdentityKey(hashedPayload);

        try {
          const response = await sparkTokenClient.freeze_tokens({
            freezeTokensPayload,
            issuerSignature,
          });

          return {
            identifier,
            response,
          };
        } catch (error) {
          throw new NetworkError(
            `Failed to send a freeze/unfreeze operation to operator: ${operator.address}`,
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
