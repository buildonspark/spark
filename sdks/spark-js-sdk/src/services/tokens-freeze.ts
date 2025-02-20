import { secp256k1 } from "@noble/curves/secp256k1";
import {
  FreezeTokensPayload,
  GetSigningOperatorListResponse,
  FreezeTokensResponse,
} from "../proto/spark";
import { ConnectionManager } from "./connection";
import { WalletConfigService } from "./config";
import {
    hashFreezeTokensPayload,
  } from "../utils/token-hashing";

import { validateResponses } from "../utils/response-validation";

export class TokenFreezeService {
  private readonly config: WalletConfigService;
  private readonly connectionManager: ConnectionManager;

  constructor(
    config: WalletConfigService,
    connectionManager: ConnectionManager
  ) {
    this.config = config;
    this.connectionManager = connectionManager;
  }

  async freezeTokens(ownerPublicKey: Uint8Array, tokenPublicKey: Uint8Array): Promise<FreezeTokensResponse> {
    return this.freezeOperation(ownerPublicKey, tokenPublicKey, false);
  }

  async unfreezeTokens(ownerPublicKey: Uint8Array, tokenPublicKey: Uint8Array): Promise<FreezeTokensResponse> {
    return this.freezeOperation(ownerPublicKey, tokenPublicKey, true);
  }

  private async freezeOperation(
    ownerPublicKey: Uint8Array,
    tokenPublicKey: Uint8Array,
    shouldUnfreeze: boolean
  ): Promise<FreezeTokensResponse> {
    const signingOperators = this.config.getConfig().signingOperators;
    const issuerProvidedTimestamp = Date.now();

    // Submit freeze_tokens to all SOs in parallel
    const freezeResponses = await Promise.allSettled(
      Object.entries(signingOperators).map(
        async ([identifier, operator]) => {
          const internalSparkClient =
            await this.connectionManager.createSparkClient(operator.address);

          const freezeTokensPayload: FreezeTokensPayload = {
            ownerPublicKey,
            tokenPublicKey,
            shouldUnfreeze,
            issuerProvidedTimestamp,
            operatorIdentityPublicKey: operator.identityPublicKey,
          };

          const hashedPayload: Uint8Array =
            hashFreezeTokensPayload(freezeTokensPayload);

          const issuerSignature =
            await this.config.signer.signMessageWithPublicKey(hashedPayload, tokenPublicKey);

          const response = await internalSparkClient.freeze_tokens({
            freezeTokensPayload,
            issuerSignature,
          });
          return {
            identifier,
            response,
          };
        }
      )
    );

    const successfulResponses = validateResponses(
      freezeResponses
    );

    return successfulResponses[0].response;
  }
}
