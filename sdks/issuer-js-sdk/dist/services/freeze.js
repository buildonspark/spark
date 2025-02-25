import { validateResponses } from "@buildonspark/spark-js-sdk/utils";
import { hashFreezeTokensPayload } from "../utils/token-hashing.js";
export class TokenFreezeService {
    config;
    connectionManager;
    constructor(config, connectionManager) {
        this.config = config;
        this.connectionManager = connectionManager;
    }
    async freezeTokens(ownerPublicKey, tokenPublicKey) {
        return this.freezeOperation(ownerPublicKey, tokenPublicKey, false);
    }
    async unfreezeTokens(ownerPublicKey, tokenPublicKey) {
        return this.freezeOperation(ownerPublicKey, tokenPublicKey, true);
    }
    async freezeOperation(ownerPublicKey, tokenPublicKey, shouldUnfreeze) {
        const signingOperators = this.config.getConfig().signingOperators;
        const issuerProvidedTimestamp = Date.now();
        // Submit freeze_tokens to all SOs in parallel
        const freezeResponses = await Promise.allSettled(Object.entries(signingOperators).map(async ([identifier, operator]) => {
            const internalSparkClient = await this.connectionManager.createSparkClient(operator.address);
            const freezeTokensPayload = {
                ownerPublicKey,
                tokenPublicKey,
                shouldUnfreeze,
                issuerProvidedTimestamp,
                operatorIdentityPublicKey: operator.identityPublicKey,
            };
            const hashedPayload = hashFreezeTokensPayload(freezeTokensPayload);
            const issuerSignature = await this.config.signer.signMessageWithIdentityKey(hashedPayload);
            const response = await internalSparkClient.freeze_tokens({
                freezeTokensPayload,
                issuerSignature,
            });
            return {
                identifier,
                response,
            };
        }));
        const successfulResponses = validateResponses(freezeResponses);
        return successfulResponses[0].response;
    }
}
//# sourceMappingURL=freeze.js.map