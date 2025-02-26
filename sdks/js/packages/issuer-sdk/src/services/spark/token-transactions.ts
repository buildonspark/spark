import { TokenTransactionService } from "@buildonspark/spark-sdk/token-transactions";
import { TokenTransaction } from "@buildonspark/spark-sdk/proto/spark";
import { ConnectionManager } from "@buildonspark/spark-sdk/connection";
import { WalletConfigService } from "@buildonspark/spark-sdk/config";
import { numberToBytesBE } from "@noble/curves/abstract/utils";

export class IssuerTokenTransactionService extends TokenTransactionService {
  constructor(
    config: WalletConfigService,
    connectionManager: ConnectionManager
  ) {
    super(config, connectionManager);
  }

  async constructMintTokenTransaction(
    tokenPublicKey: Uint8Array,
    tokenAmount: bigint
  ): Promise<TokenTransaction> {
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
      sparkOperatorIdentityPublicKeys:
        super.collectOperatorIdentityPublicKeys(),
    };
  }
}
