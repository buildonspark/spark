import { TokenTransactionService } from "@buildonspark/spark-js-sdk/token-transactions";
import { numberToBytesBE } from "@noble/curves/abstract/utils";
export class IssuerTokenTransactionService extends TokenTransactionService {
    constructor(config, connectionManager) {
        super(config, connectionManager);
    }
    async constructMintTokenTransaction(tokenPublicKey, tokenAmount) {
        return {
            tokenInput: {
                $case: "mintInput",
                mintInput: {
                    issuerPublicKey: tokenPublicKey,
                    issuerProvidedTimestamp: Date.now(),
                },
            },
            outputLeaves: [
                {
                    ownerPublicKey: tokenPublicKey,
                    tokenPublicKey: tokenPublicKey,
                    tokenAmount: numberToBytesBE(tokenAmount, 16),
                },
            ],
            sparkOperatorIdentityPublicKeys: super.collectOperatorIdentityPublicKeys(),
        };
    }
}
//# sourceMappingURL=token-transactions.js.map