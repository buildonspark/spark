import {
  TokenTransactionService,
  WalletConfigService,
  type BaseConnectionManager,
} from "@buildonspark/spark-sdk";
import { TokenTransaction } from "@buildonspark/spark-sdk/proto/spark_token";
import { numberToBytesBE } from "@noble/curves/utils";

export class IssuerTokenTransactionService extends TokenTransactionService {
  constructor(
    config: WalletConfigService,
    connectionManager: BaseConnectionManager,
  ) {
    super(config, connectionManager);
  }

  async constructMintTokenTransaction(
    rawTokenIdentifierBytes: Uint8Array,
    issuerTokenPublicKey: Uint8Array,
    tokenAmount: bigint,
  ): Promise<TokenTransaction> {
    return {
      version: 2,
      network: this.config.getNetworkProto(),
      tokenInputs: {
        $case: "mintInput",
        mintInput: {
          issuerPublicKey: issuerTokenPublicKey,
          tokenIdentifier: rawTokenIdentifierBytes,
        },
      },
      tokenOutputs: [
        {
          ownerPublicKey: issuerTokenPublicKey,
          tokenIdentifier: rawTokenIdentifierBytes,
          tokenAmount: numberToBytesBE(tokenAmount, 16),
        },
      ],
      clientCreatedTimestamp: new Date(),
      sparkOperatorIdentityPublicKeys:
        super.collectOperatorIdentityPublicKeys(),
      expiryTime: undefined,
      invoiceAttachments: [],
    };
  }

  async constructCreateTokenTransaction(
    tokenPublicKey: Uint8Array,
    tokenName: string,
    tokenTicker: string,
    decimals: number,
    maxSupply: bigint,
    isFreezable: boolean,
  ): Promise<TokenTransaction> {
    return {
      version: 2,
      network: this.config.getNetworkProto(),
      tokenInputs: {
        $case: "createInput",
        createInput: {
          issuerPublicKey: tokenPublicKey,
          tokenName: tokenName,
          tokenTicker: tokenTicker,
          decimals: decimals,
          maxSupply: numberToBytesBE(maxSupply, 16),
          isFreezable: isFreezable,
        },
      },
      tokenOutputs: [],
      clientCreatedTimestamp: new Date(),
      sparkOperatorIdentityPublicKeys:
        super.collectOperatorIdentityPublicKeys(),
      expiryTime: undefined,
      invoiceAttachments: [],
    };
  }
}
