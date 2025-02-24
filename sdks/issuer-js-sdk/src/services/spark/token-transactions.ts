import { TokenTransactionService } from "@buildonspark/spark-js-sdk/token-transactions";
import {
  TokenTransaction,
  LeafWithPreviousTransactionData,
} from "../../proto/spark.js";
import { ConnectionManager } from "@buildonspark/spark-js-sdk/connection";
import { WalletConfigService } from "@buildonspark/spark-js-sdk/config";
import { getTokenLeavesSum } from "@buildonspark/spark-js-sdk/utils";
import { numberToBytesBE } from "@noble/curves/abstract/utils";

const BURN_ADDRESS = new Uint8Array(32).fill(0x02);

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

  async constructBurnTokenTransaction(
    selectedLeaves: LeafWithPreviousTransactionData[],
    tokenPublicKey: Uint8Array,
    tokenAmount: bigint,
    transferBackToIdentityPublicKey: boolean = false
  ) {
    const tokenAmountSum = getTokenLeavesSum(selectedLeaves);

    let transferTokenTransaction: TokenTransaction;

    if (tokenAmount > tokenAmountSum) {
      throw new Error("Not enough tokens to burn");
    } else if (tokenAmount === tokenAmountSum) {
      transferTokenTransaction = {
        tokenInput: {
          $case: "transferInput",
          transferInput: {
            leavesToSpend: selectedLeaves.map((leaf) => ({
              prevTokenTransactionHash: leaf.previousTransactionHash,
              prevTokenTransactionLeafVout: leaf.previousTransactionVout,
            })),
          },
        },
        outputLeaves: [
          {
            ownerPublicKey: BURN_ADDRESS,
            tokenPublicKey: tokenPublicKey,
            tokenAmount: numberToBytesBE(tokenAmountSum, 16),
          },
        ],
        sparkOperatorIdentityPublicKeys:
          super.collectOperatorIdentityPublicKeys(),
      };
    } else {
      const tokenDifferenceToSendBack = tokenAmountSum - tokenAmount;

      transferTokenTransaction = {
        tokenInput: {
          $case: "transferInput",
          transferInput: {
            leavesToSpend: selectedLeaves.map((leaf) => ({
              prevTokenTransactionHash: leaf.previousTransactionHash,
              prevTokenTransactionLeafVout: leaf.previousTransactionVout,
            })),
          },
        },
        outputLeaves: [
          {
            ownerPublicKey: BURN_ADDRESS,
            tokenPublicKey: tokenPublicKey,
            tokenAmount: numberToBytesBE(tokenAmount, 16),
          },
          {
            ownerPublicKey: transferBackToIdentityPublicKey
              ? await this.config.signer.getIdentityPublicKey()
              : await this.config.signer.generatePublicKey(),
            tokenPublicKey: tokenPublicKey,
            tokenAmount: numberToBytesBE(tokenDifferenceToSendBack, 16),
          },
        ],
        sparkOperatorIdentityPublicKeys:
          super.collectOperatorIdentityPublicKeys(),
      };
    }

    return transferTokenTransaction;
  }
}
