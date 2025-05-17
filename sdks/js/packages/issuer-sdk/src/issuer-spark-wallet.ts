import { TokenPubkey, TokenPubkeyAnnouncement } from "@buildonspark/lrc20-sdk";
import {
  ListAllTokenTransactionsCursor,
  OperationType,
} from "@buildonspark/lrc20-sdk/proto/rpc/v1/types";
import {
  NetworkError,
  SparkWallet,
  SparkWalletProps,
  ValidationError,
} from "@buildonspark/spark-sdk";
import {
  decodeSparkAddress,
  encodeSparkAddress,
} from "@buildonspark/spark-sdk/address";
import { OutputWithPreviousTransactionData } from "@buildonspark/spark-sdk/proto/spark";
import { ConfigOptions } from "@buildonspark/spark-sdk/services/wallet-config";
import {
  bytesToHex,
  bytesToNumberBE,
  hexToBytes,
} from "@noble/curves/abstract/utils";
import { TokenFreezeService } from "./services/freeze.js";
import { IssuerTokenTransactionService } from "./services/token-transactions.js";
import { GetTokenActivityResponse, TokenDistribution } from "./types.js";
import { convertTokenActivityToHexEncoded } from "./utils/type-mappers.js";
import { NotImplementedError } from "@buildonspark/spark-sdk";

const BURN_ADDRESS = "02".repeat(33);

export type IssuerTokenInfo = {
  tokenPublicKey: string;
  tokenName: string;
  tokenSymbol: string;
  tokenDecimals: number;
  maxSupply: bigint;
  isFreezable: boolean;
};

/**
 * Represents a Spark wallet with minting capabilities.
 * This class extends the base SparkWallet with additional functionality for token minting,
 * burning, and freezing operations.
 */
export class IssuerSparkWallet extends SparkWallet {
  private issuerTokenTransactionService: IssuerTokenTransactionService;
  private tokenFreezeService: TokenFreezeService;

  /**
   * Initializes a new IssuerSparkWallet instance.
   * @param options - Configuration options for the wallet
   * @returns An object containing the initialized wallet and initialization response
   */
  public static async initialize(options: SparkWalletProps) {
    const wallet = new IssuerSparkWallet(options.options);

    const initResponse = await wallet.initWallet(
      options.mnemonicOrSeed,
      options.accountNumber,
    );
    return {
      wallet,
      ...initResponse,
    };
  }

  protected constructor(configOptions?: ConfigOptions) {
    super(configOptions);
    this.issuerTokenTransactionService = new IssuerTokenTransactionService(
      this.config,
      this.connectionManager,
    );
    this.tokenFreezeService = new TokenFreezeService(
      this.config,
      this.connectionManager,
    );
  }

  /**
   * Gets the token balance for the issuer's token.
   * @returns An object containing the token balance as a bigint
   */
  public async getIssuerTokenBalance(): Promise<{
    balance: bigint;
  }> {
    const publicKey = await super.getIdentityPublicKey();
    const balanceObj = await this.getBalance();

    if (!balanceObj.tokenBalances || !balanceObj.tokenBalances.has(publicKey)) {
      return {
        balance: 0n,
      };
    }
    return {
      balance: balanceObj.tokenBalances.get(publicKey)!.balance,
    };
  }

  /**
   * Retrieves information about the issuer's token.
   * @returns An object containing token information including public key, name, symbol, decimals, max supply, and freeze status
   * @throws {NetworkError} If the token info cannot be retrieved
   */
  public async getIssuerTokenInfo(): Promise<IssuerTokenInfo | null> {
    const lrc20Client = await this.lrc20ConnectionManager.createLrc20Client();

    try {
      const tokenInfo = await lrc20Client.getTokenPubkeyInfo({
        publicKeys: [hexToBytes(await super.getIdentityPublicKey())],
      });

      const info = tokenInfo.tokenPubkeyInfos[0];
      return {
        tokenPublicKey: bytesToHex(info.announcement!.publicKey!.publicKey),
        tokenName: info.announcement!.name,
        tokenSymbol: info.announcement!.symbol,
        tokenDecimals: Number(bytesToNumberBE(info.announcement!.decimal)),
        isFreezable: info.announcement!.isFreezable,
        maxSupply: bytesToNumberBE(info.announcement!.maxSupply),
      };
    } catch (error) {
      throw new NetworkError("Failed to get token info", {
        operation: "getIssuerTokenInfo",
        errorCount: 1,
        errors: error instanceof Error ? error.message : String(error),
      });
    }
  }

  /**
   * Mints new tokens
   * @param tokenAmount - The amount of tokens to mint
   * @returns The transaction ID of the mint operation
   */
  public async mintTokens(tokenAmount: bigint): Promise<string> {
    var tokenPublicKey = await super.getIdentityPublicKey();

    const tokenTransaction =
      await this.issuerTokenTransactionService.constructMintTokenTransaction(
        hexToBytes(tokenPublicKey),
        tokenAmount,
      );

    return await this.issuerTokenTransactionService.broadcastTokenTransaction(
      tokenTransaction,
    );
  }

  /**
   * Burns issuer's tokens
   * @param tokenAmount - The amount of tokens to burn
   * @param selectedOutputs - Optional array of outputs to use for the burn operation
   * @returns The transaction ID of the burn operation
   */
  public async burnTokens(
    tokenAmount: bigint,
    selectedOutputs?: OutputWithPreviousTransactionData[],
  ): Promise<string> {
    const burnAddress = encodeSparkAddress({
      identityPublicKey: BURN_ADDRESS,
      network: this.config.getNetworkType(),
    });
    return await this.transferTokens({
      tokenPublicKey: await super.getIdentityPublicKey(),
      tokenAmount,
      receiverSparkAddress: burnAddress,
      selectedOutputs,
    });
  }

  /**
   * Freezes tokens associated with a specific Spark address.
   * @param sparkAddress - The Spark address whose tokens should be frozen
   * @returns An object containing the IDs of impacted outputs and the total amount of frozen tokens
   */
  public async freezeTokens(
    sparkAddress: string,
  ): Promise<{ impactedOutputIds: string[]; impactedTokenAmount: bigint }> {
    await this.syncTokenOutputs();
    const tokenPublicKey = await super.getIdentityPublicKey();
    const decodedOwnerPubkey = decodeSparkAddress(
      sparkAddress,
      this.config.getNetworkType(),
    );
    const response = await this.tokenFreezeService!.freezeTokens(
      hexToBytes(decodedOwnerPubkey),
      hexToBytes(tokenPublicKey),
    );

    // Convert the Uint8Array to a bigint
    const tokenAmount = bytesToNumberBE(response.impactedTokenAmount);

    return {
      impactedOutputIds: response.impactedOutputIds,
      impactedTokenAmount: tokenAmount,
    };
  }

  /**
   * Unfreezes previously frozen tokens associated with a specific Spark address.
   * @param sparkAddress - The Spark address whose tokens should be unfrozen
   * @returns An object containing the IDs of impacted outputs and the total amount of unfrozen tokens
   */
  public async unfreezeTokens(
    sparkAddress: string,
  ): Promise<{ impactedOutputIds: string[]; impactedTokenAmount: bigint }> {
    await this.syncTokenOutputs();
    const tokenPublicKey = await super.getIdentityPublicKey();
    const decodedOwnerPubkey = decodeSparkAddress(
      sparkAddress,
      this.config.getNetworkType(),
    );
    const response = await this.tokenFreezeService!.unfreezeTokens(
      hexToBytes(decodedOwnerPubkey),
      hexToBytes(tokenPublicKey),
    );
    const tokenAmount = bytesToNumberBE(response.impactedTokenAmount);

    return {
      impactedOutputIds: response.impactedOutputIds,
      impactedTokenAmount: tokenAmount,
    };
  }

  /**
   * Retrieves the activity history for the issuer's token.
   * @param pageSize - The number of transactions to return per page (default: 100)
   * @param cursor - Optional cursor for pagination
   * @param operationTypes - Optional array of operation types to filter by
   * @param beforeTimestamp - Optional timestamp to filter transactions before
   * @param afterTimestamp - Optional timestamp to filter transactions after
   * @returns An object containing the token activity data
   * @throws {ValidationError} If pageSize is not a safe integer
   * @throws {NetworkError} If the activity data cannot be retrieved
   */
  public async getIssuerTokenActivity(
    pageSize: number = 100,
    cursor?: ListAllTokenTransactionsCursor,
    operationTypes?: OperationType[],
    beforeTimestamp?: Date,
    afterTimestamp?: Date,
  ): Promise<GetTokenActivityResponse> {
    if (!Number.isSafeInteger(pageSize)) {
      throw new ValidationError("pageSize must be less than 2^53", {
        field: "pageSize",
        value: pageSize,
        expected: "smaller or equal to " + Number.MAX_SAFE_INTEGER,
      });
    }

    const lrc20Client = await this.lrc20ConnectionManager.createLrc20Client();

    try {
      const transactions = await lrc20Client.listTransactions({
        tokenPublicKey: hexToBytes(await super.getIdentityPublicKey()),
        cursor,
        pageSize,
        beforeTimestamp,
        afterTimestamp,
        operationTypes,
      });

      return convertTokenActivityToHexEncoded(transactions);
    } catch (error) {
      throw new NetworkError("Failed to get token activity", {
        operation: "listTransactions",
        errorCount: 1,
        errors: error instanceof Error ? error.message : String(error),
      });
    }
  }

  /**
   * Retrieves the distribution information for the issuer's token.
   * @throws {NotImplementedError} This feature is not yet supported
   */
  public async getIssuerTokenDistribution(): Promise<TokenDistribution> {
    throw new NotImplementedError("Token distribution is not yet supported");
  }

  /**
   * Announces a new token on the L1 (Bitcoin) network.
   * @param tokenName - The name of the token
   * @param tokenTicker - The ticker symbol for the token
   * @param decimals - The number of decimal places for the token
   * @param maxSupply - The maximum supply of the token
   * @param isFreezable - Whether the token can be frozen
   * @param feeRateSatsPerVb - The fee rate in satoshis per virtual byte (default: 4.0)
   * @returns The transaction ID of the announcement
   * @throws {ValidationError} If decimals is not a safe integer
   * @throws {NetworkError} If the announcement transaction cannot be broadcast
   */
  public async announceTokenL1(
    tokenName: string,
    tokenTicker: string,
    decimals: number,
    maxSupply: bigint,
    isFreezable: boolean,
    feeRateSatsPerVb: number = 4.0,
  ): Promise<string> {
    if (!Number.isSafeInteger(decimals)) {
      throw new ValidationError("Decimals must be less than 2^53", {
        field: "decimals",
        value: decimals,
        expected: "smaller or equal to " + Number.MAX_SAFE_INTEGER,
      });
    }

    await this.lrc20Wallet!.syncWallet();
    const tokenPublicKey = new TokenPubkey(this.lrc20Wallet!.pubkey);

    const announcement = new TokenPubkeyAnnouncement(
      tokenPublicKey,
      tokenName,
      tokenTicker,
      decimals,
      maxSupply,
      isFreezable,
    );

    try {
      const tx = await this.lrc20Wallet!.prepareAnnouncement(
        announcement,
        feeRateSatsPerVb,
      );

      return await this.lrc20Wallet!.broadcastRawBtcTransaction(
        tx.bitcoin_tx.toHex(),
      );
    } catch (error) {
      throw new NetworkError(
        "Failed to broadcast announcement transaction on L1",
        {
          operation: "broadcastRawBtcTransaction",
          errorCount: 1,
          errors: error instanceof Error ? error.message : String(error),
        },
      );
    }
  }
}
