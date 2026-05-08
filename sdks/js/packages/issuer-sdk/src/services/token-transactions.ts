import {
  TokenTransactionService,
  type WalletConfigService,
  type BaseConnectionManager,
} from "@buildonspark/spark-sdk";
import {
  type PartialTokenTransaction,
  type TokenTransaction,
} from "@buildonspark/spark-sdk/proto/spark_token";
import { numberToBytesBE } from "@noble/curves/utils";

export class IssuerTokenTransactionService extends TokenTransactionService {
  constructor(
    config: WalletConfigService,
    connectionManager: BaseConnectionManager,
  ) {
    super(config, connectionManager);
  }

  constructMintTokenTransaction(
    rawTokenIdentifierBytes: Uint8Array,
    issuerTokenPublicKey: Uint8Array,
    tokenAmount: bigint,
  ): Promise<TokenTransaction> {
    return new Promise((resolve) => {
      resolve({
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
      });
    });
  }

  constructPartialMintTokenTransaction(
    rawTokenIdentifierBytes: Uint8Array,
    issuerTokenPublicKey: Uint8Array,
    tokenAmount: bigint,
  ): Promise<PartialTokenTransaction> {
    return new Promise((resolve) => {
      resolve({
        version: 3,
        tokenTransactionMetadata: {
          network: this.config.getNetworkProto(),
          sparkOperatorIdentityPublicKeys:
            this.collectOperatorIdentityPublicKeys(),
          validityDurationSeconds:
            this.config.getTokenValidityDurationSeconds(),
          clientCreatedTimestamp: this.connectionManager.getCurrentServerTime(),
          invoiceAttachments: [],
        },
        tokenInputs: {
          $case: "mintInput",
          mintInput: {
            issuerPublicKey: issuerTokenPublicKey,
            tokenIdentifier: rawTokenIdentifierBytes,
          },
        },
        partialTokenOutputs: [
          {
            ownerPublicKey: issuerTokenPublicKey,
            tokenIdentifier: rawTokenIdentifierBytes,
            withdrawBondSats: this.config.getExpectedWithdrawBondSats(),
            withdrawRelativeBlockLocktime:
              this.config.getExpectedWithdrawRelativeBlockLocktime(),
            tokenAmount: numberToBytesBE(tokenAmount, 16),
          },
        ],
      });
    });
  }

  constructCreateTokenTransaction(
    tokenPublicKey: Uint8Array,
    tokenName: string,
    tokenTicker: string,
    decimals: number,
    maxSupply: bigint,
    isFreezable: boolean,
    extraMetadata: Uint8Array | undefined,
  ): Promise<TokenTransaction> {
    return new Promise((resolve) => {
      resolve({
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
            extraMetadata: extraMetadata,
          },
        },
        tokenOutputs: [],
        clientCreatedTimestamp: new Date(),
        sparkOperatorIdentityPublicKeys:
          super.collectOperatorIdentityPublicKeys(),
        expiryTime: undefined,
        invoiceAttachments: [],
      });
    });
  }

  constructPartialCreateTokenTransaction(
    tokenPublicKey: Uint8Array,
    tokenName: string,
    tokenTicker: string,
    decimals: number,
    maxSupply: bigint,
    isFreezable: boolean,
    extraMetadata?: Uint8Array,
  ): Promise<PartialTokenTransaction> {
    return new Promise((resolve) => {
      resolve({
        version: 3,
        tokenTransactionMetadata: {
          network: this.config.getNetworkProto(),
          sparkOperatorIdentityPublicKeys:
            this.collectOperatorIdentityPublicKeys(),
          validityDurationSeconds:
            this.config.getTokenValidityDurationSeconds(),
          clientCreatedTimestamp: this.connectionManager.getCurrentServerTime(),
          invoiceAttachments: [],
        },
        tokenInputs: {
          $case: "createInput",
          createInput: {
            issuerPublicKey: tokenPublicKey,
            tokenName: tokenName,
            tokenTicker: tokenTicker,
            decimals: decimals,
            maxSupply: numberToBytesBE(maxSupply, 16),
            isFreezable: isFreezable,
            extraMetadata: extraMetadata,
          },
        },
        partialTokenOutputs: [],
      });
    });
  }
}
