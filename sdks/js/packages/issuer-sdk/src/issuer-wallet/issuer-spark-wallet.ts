import {
  Bech32mTokenIdentifier,
  decodeBech32mTokenIdentifier,
  decodeSparkAddress,
  encodeBech32mTokenIdentifier,
  encodeSparkAddress,
  SparkError,
  SparkSigner,
  SparkWallet,
  SparkRequestError,
  SparkValidationError,
  type ConfigOptions,
} from "@buildonspark/spark-sdk";
import { OutputWithPreviousTransactionData } from "@buildonspark/spark-sdk/proto/spark_token";
import { bytesToHex, bytesToNumberBE, hexToBytes } from "@noble/curves/utils";
import { TokenFreezeService } from "../services/freeze.js";
import { IssuerTokenTransactionService } from "../services/token-transactions.js";
import { validateTokenParameters } from "../utils/create-validation.js";
import { IssuerTokenMetadata, TokenDistribution } from "./types.js";

const BURN_ADDRESS = "02".repeat(33);

/**
 * Represents a Spark wallet with minting capabilities.
 * This class extends the base SparkWallet with additional functionality for token minting,
 * burning, and freezing operations.
 */
export abstract class IssuerSparkWallet extends SparkWallet {
  private issuerTokenTransactionService: IssuerTokenTransactionService;
  private tokenFreezeService: TokenFreezeService;
  protected tracerId = "issuer-sdk";

  /**
   * Initializes a new IssuerSparkWallet instance.
   * Inherits the generic static initialize from the base class.
   */

  constructor(configOptions?: ConfigOptions, signer?: SparkSigner) {
    super(configOptions, signer);
    this.issuerTokenTransactionService = new IssuerTokenTransactionService(
      this.config,
      this.connectionManager,
    );
    this.tokenFreezeService = new TokenFreezeService(
      this.config,
      this.connectionManager,
    );
    this.wrapIssuerSparkWalletMethods();
  }

  /**
   * Gets the token balance for the issuer's token.
   * @returns An object containing the token balance as a bigint
   */
  public async getIssuerTokenBalance(): Promise<{
    tokenIdentifier: Bech32mTokenIdentifier | undefined;
    balance: bigint;
  }> {
    const publicKey = await super.getIdentityPublicKey();
    const balanceObj = await this.getBalance();
    const issuerBalance = [...balanceObj.tokenBalances.entries()].find(
      ([, info]) => info.tokenMetadata.tokenPublicKey === publicKey,
    ); // [tokenIdentifier, { balance, tokenMetadata }]

    if (!balanceObj.tokenBalances || issuerBalance === undefined) {
      return {
        tokenIdentifier: undefined,
        balance: 0n,
      };
    }
    return {
      tokenIdentifier: issuerBalance[0] ?? undefined,
      balance: issuerBalance[1].balance,
    };
  }

  /**
   * Retrieves information about the issuer's token.
   * @returns An object containing token information including public key, name, symbol, decimals, max supply, and freeze status
   * @throws {SparkRequestError} If the token metadata cannot be retrieved
   */
  public async getIssuerTokenMetadata(): Promise<IssuerTokenMetadata> {
    const issuerPublicKey = await super.getIdentityPublicKey();
    const tokenMetadata = this.tokenMetadata;

    const cachedIssuerTokenMetadata = [...tokenMetadata.entries()].find(
      ([, metadata]) =>
        bytesToHex(metadata.issuerPublicKey) === issuerPublicKey,
    );
    if (cachedIssuerTokenMetadata !== undefined) {
      const metadata = cachedIssuerTokenMetadata[1];
      return {
        tokenPublicKey: bytesToHex(metadata.issuerPublicKey),
        rawTokenIdentifier: metadata.tokenIdentifier,
        tokenName: metadata.tokenName,
        tokenTicker: metadata.tokenTicker,
        decimals: metadata.decimals,
        maxSupply: bytesToNumberBE(metadata.maxSupply),
        isFreezable: metadata.isFreezable,
        extraMetadata: metadata.extraMetadata
          ? new Uint8Array(metadata.extraMetadata)
          : undefined,
      };
    }

    const sparkTokenClient =
      await this.connectionManager.createSparkTokenClient(
        this.config.getCoordinatorAddress(),
      );
    try {
      const response = await sparkTokenClient.query_token_metadata({
        issuerPublicKeys: Array.of(hexToBytes(issuerPublicKey)),
      });
      if (response.tokenMetadata.length === 0) {
        throw new SparkValidationError(
          "Token metadata not found - If a token has not yet been created, please create it first. Try again in a few seconds.",
          {
            field: "tokenMetadata",
            value: response.tokenMetadata,
            expected: "non-empty array",
            actualLength: response.tokenMetadata.length,
            expectedLength: 1,
          },
        );
      }
      const metadata = response.tokenMetadata[0];
      const tokenIdentifier = encodeBech32mTokenIdentifier({
        tokenIdentifier: metadata.tokenIdentifier,
        network: this.config.getNetworkType(),
      });
      this.tokenMetadata.set(tokenIdentifier, metadata);

      return {
        tokenPublicKey: bytesToHex(metadata.issuerPublicKey),
        rawTokenIdentifier: metadata.tokenIdentifier,
        tokenName: metadata.tokenName,
        tokenTicker: metadata.tokenTicker,
        decimals: metadata.decimals,
        maxSupply: bytesToNumberBE(metadata.maxSupply),
        isFreezable: metadata.isFreezable,
        extraMetadata: metadata.extraMetadata,
      };
    } catch (error) {
      throw new SparkRequestError("Failed to fetch token metadata", { error });
    }
  }

  /**
   * Retrieves the bech32m encoded token identifier for the issuer's token.
   * @returns The bech32m encoded token identifier for the issuer's token
   * @throws {SparkRequestError} If the token identifier cannot be retrieved
   */
  public async getIssuerTokenIdentifier(): Promise<Bech32mTokenIdentifier> {
    const tokenMetadata = await this.getIssuerTokenMetadata();

    return encodeBech32mTokenIdentifier({
      tokenIdentifier: tokenMetadata.rawTokenIdentifier,
      network: this.config.getNetworkType(),
    });
  }

  /**
   * Create a new token on Spark.
   *
   * @param params - Object containing token creation parameters.
   * @param params.tokenName - The name of the token.
   * @param params.tokenTicker - The ticker symbol for the token.
   * @param params.decimals - The number of decimal places for the token.
   * @param params.isFreezable - Whether the token can be frozen.
   * @param [params.maxSupply=0n] - (Optional) The maximum supply of the token. Defaults to <code>0n</code>.
   * @param params.extraMetadata - (Optional) This can be used to store additional bytes data to be associated with a token, like image data.
   *
   * @returns The transaction ID of the announcement.
   *
   * @throws {SparkValidationError} If `decimals` is not a safe integer or other validation fails.
   * @throws {SparkRequestError} If the announcement transaction cannot be broadcast.
   */
  public async createToken({
    tokenName,
    tokenTicker,
    decimals,
    isFreezable,
    maxSupply = 0n,
    extraMetadata,
  }: {
    tokenName: string;
    tokenTicker: string;
    decimals: number;
    isFreezable: boolean;
    maxSupply?: bigint;
    extraMetadata?: Uint8Array;
  }): Promise<string> {
    validateTokenParameters(
      tokenName,
      tokenTicker,
      decimals,
      maxSupply,
      extraMetadata,
    );

    const issuerPublicKey = await super.getIdentityPublicKey();

    if (this.config.getTokenTransactionVersion() === "V2") {
      const tokenTransaction =
        await this.issuerTokenTransactionService.constructCreateTokenTransaction(
          hexToBytes(issuerPublicKey),
          tokenName,
          tokenTicker,
          decimals,
          maxSupply,
          isFreezable,
          extraMetadata,
        );

      return await this.issuerTokenTransactionService.broadcastTokenTransaction(
        tokenTransaction,
      );
    } else {
      const partialTokenTransaction =
        await this.issuerTokenTransactionService.constructPartialCreateTokenTransaction(
          hexToBytes(issuerPublicKey),
          tokenName,
          tokenTicker,
          decimals,
          maxSupply,
          isFreezable,
          extraMetadata,
        );

      return await this.issuerTokenTransactionService.broadcastTokenTransactionV3(
        partialTokenTransaction,
      );
    }
  }

  /**
   * Mints new tokens
   * @param tokenAmount - The amount of tokens to mint
   * @returns The transaction ID of the mint operation
   */
  public async mintTokens(tokenAmount: bigint): Promise<string> {
    const issuerTokenPublicKey = await super.getIdentityPublicKey();
    const issuerTokenPublicKeyBytes = hexToBytes(issuerTokenPublicKey);

    const tokenMetadata = await this.getIssuerTokenMetadata();
    const rawTokenIdentifier: Uint8Array = tokenMetadata.rawTokenIdentifier;

    if (this.config.getTokenTransactionVersion() === "V2") {
      const tokenTransaction =
        await this.issuerTokenTransactionService.constructMintTokenTransaction(
          rawTokenIdentifier,
          issuerTokenPublicKeyBytes,
          tokenAmount,
        );

      return await this.issuerTokenTransactionService.broadcastTokenTransaction(
        tokenTransaction,
      );
    } else {
      const partialTokenTransaction =
        await this.issuerTokenTransactionService.constructPartialMintTokenTransaction(
          rawTokenIdentifier,
          issuerTokenPublicKeyBytes,
          tokenAmount,
        );

      return await this.issuerTokenTransactionService.broadcastTokenTransactionV3(
        partialTokenTransaction,
      );
    }
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
    const issuerTokenIdentifier: Bech32mTokenIdentifier =
      await this.getIssuerTokenIdentifier();

    return await this.transferTokens({
      tokenIdentifier: issuerTokenIdentifier,
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
    const decodedOwnerPubkey = decodeSparkAddress(
      sparkAddress,
      this.config.getNetworkType(),
    );

    const issuerTokenIdentifier = await this.getIssuerTokenIdentifier();

    const rawTokenIdentifier = decodeBech32mTokenIdentifier(
      issuerTokenIdentifier,
      this.config.getNetworkType(),
    ).tokenIdentifier;

    const response = await this.tokenFreezeService!.freezeTokens({
      ownerPublicKey: hexToBytes(decodedOwnerPubkey.identityPublicKey),
      tokenIdentifier: rawTokenIdentifier,
    });

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
    const decodedOwnerPubkey = decodeSparkAddress(
      sparkAddress,
      this.config.getNetworkType(),
    );

    const issuerTokenIdentifier = await this.getIssuerTokenIdentifier();

    const rawTokenIdentifier = decodeBech32mTokenIdentifier(
      issuerTokenIdentifier,
      this.config.getNetworkType(),
    ).tokenIdentifier;

    const response = await this.tokenFreezeService!.unfreezeTokens({
      ownerPublicKey: hexToBytes(decodedOwnerPubkey.identityPublicKey),
      tokenIdentifier: rawTokenIdentifier,
    });
    const tokenAmount = bytesToNumberBE(response.impactedTokenAmount);

    return {
      impactedOutputIds: response.impactedOutputIds,
      impactedTokenAmount: tokenAmount,
    };
  }

  /**
   * Retrieves the distribution information for the issuer's token.
   * @throws {SparkError} This feature is not yet supported
   */
  public async getIssuerTokenDistribution(): Promise<TokenDistribution> {
    throw new SparkError("Token distribution is not yet supported");
  }

  protected getTraceName(methodName: string) {
    return `IssuerSparkWallet.${methodName}`;
  }

  private wrapIssuerPublicMethod<M extends keyof IssuerSparkWallet>(
    methodName: M,
  ) {
    const original = this[methodName];

    if (typeof original !== "function") {
      throw new Error(
        `Method ${methodName} is not a function on IssuerSparkWallet.`,
      );
    }

    const originalFn = original as (...args: unknown[]) => Promise<unknown>;
    const wrapped = SparkWallet.wrapMethod(
      String(methodName),
      originalFn,
      this,
    ) as IssuerSparkWallet[M];

    (this as IssuerSparkWallet)[methodName] = wrapped;
  }

  private wrapIssuerSparkWalletMethods() {
    PUBLIC_ISSUER_SPARK_WALLET_METHODS.forEach((m) =>
      this.wrapIssuerPublicMethod(m),
    );
  }
}

type AssertNever<T extends never> = T;

type IssuerSparkWalletFunctionKeys = Extract<
  {
    [K in keyof IssuerSparkWallet]: IssuerSparkWallet[K] extends (
      ...args: any[]
    ) => PromiseLike<unknown>
      ? /* Exclude SparkWallet methods that are already wrapped by the base class: */
        K extends keyof SparkWallet
        ? never
        : K
      : never;
  }[keyof IssuerSparkWallet],
  string
>;

const PUBLIC_ISSUER_SPARK_WALLET_METHODS = [
  "getIssuerTokenBalance",
  "getIssuerTokenMetadata",
  "getIssuerTokenIdentifier",
  "createToken",
  "mintTokens",
  "burnTokens",
  "freezeTokens",
  "unfreezeTokens",
  "getIssuerTokenDistribution",
] as const satisfies readonly IssuerSparkWalletFunctionKeys[];

/* Type guard to ensure all public methods are in PUBLIC_ISSUER_SPARK_WALLET_METHODS */
type _AllIssuerMethodsCovered = AssertNever<
  Exclude<
    IssuerSparkWalletFunctionKeys,
    (typeof PUBLIC_ISSUER_SPARK_WALLET_METHODS)[number]
  >
>;
