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
import { hashFinalTokenTransaction } from "@buildonspark/spark-sdk";
import { validateTokenParameters } from "../utils/create-validation.js";
import {
  IssuerTokenMetadata,
  TokenCreationDetails,
  TokenDistribution,
} from "./types.js";

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
   * @deprecated Use getIssuerTokenBalances() instead. This method will be removed in a future version.
   * @returns An object containing the token balance as a bigint
   *
   * @throws {SparkValidationError} If multiple tokens are found for this issuer
   */
  public async getIssuerTokenBalance(): Promise<{
    tokenIdentifier: Bech32mTokenIdentifier | undefined;
    balance: bigint;
  }> {
    const publicKey = await super.getIdentityPublicKey();
    const balanceObj = await this.getBalance();
    const issuerBalance = [...balanceObj.tokenBalances.entries()].filter(
      ([, info]) => info.tokenMetadata.tokenPublicKey === publicKey,
    ); // [tokenIdentifier, { balance, tokenMetadata }]

    if (issuerBalance.length > 1) {
      throw new SparkValidationError(
        "Multiple tokens found for this issuer. Use getIssuerTokenBalances() instead.",
        {
          field: "issuerTokenBalance",
          expected: "single token",
          actual: `${issuerBalance.length} tokens`,
        },
      );
    }

    if (issuerBalance.length === 0) {
      return {
        tokenIdentifier: undefined,
        balance: 0n,
      };
    }

    return {
      tokenIdentifier: issuerBalance[0][0],
      balance: issuerBalance[0][1].balance,
    };
  }

  /**
   * Gets the token balances for the tokens that were issued by this user.
   * @returns An array of objects containing the token identifier and balance
   */
  public async getIssuerTokenBalances(): Promise<
    {
      tokenIdentifier: Bech32mTokenIdentifier | undefined;
      balance: bigint;
    }[]
  > {
    const publicKey = await super.getIdentityPublicKey();
    const balanceObj = await this.getBalance();
    const issuerBalance = [...balanceObj.tokenBalances.entries()].filter(
      ([, info]) => info.tokenMetadata.tokenPublicKey === publicKey,
    ); // [tokenIdentifier, { balance, tokenMetadata }]

    if (issuerBalance.length === 0) {
      return [
        {
          tokenIdentifier: undefined,
          balance: 0n,
        },
      ];
    }

    return issuerBalance.map(([tokenIdentifier, { balance }]) => ({
      tokenIdentifier,
      balance,
    }));
  }

  /**
   * Retrieves information about the issuer's token.
   * @deprecated Use getIssuerTokensMetadata() instead. This method will be removed in a future version.
   * @returns An object containing token information including public key, name, symbol, decimals, max supply, freeze status, and extra metadata
   * @throws {SparkRequestError} If the token metadata cannot be retrieved
   * @throws {SparkValidationError} If multiple tokens are found for this issuer
   */
  public async getIssuerTokenMetadata(): Promise<IssuerTokenMetadata> {
    const issuerPublicKey = await super.getIdentityPublicKey();

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
      if (response.tokenMetadata.length > 1) {
        throw new SparkValidationError(
          "Multiple tokens found for this issuer. Please migrate to getIssuerTokensMetadata() instead.",
          {
            field: "tokenMetadata",
            value: response.tokenMetadata,
          },
        );
      }

      const metadata = response.tokenMetadata[0];
      const bech32mTokenIdentifier = encodeBech32mTokenIdentifier({
        tokenIdentifier: metadata.tokenIdentifier,
        network: this.config.getNetworkType(),
      });
      this.tokenMetadata.set(bech32mTokenIdentifier, metadata);

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
        bech32mTokenIdentifier,
      };
    } catch (error) {
      throw new SparkRequestError("Failed to fetch token metadata", { error });
    }
  }

  /**
   * Retrieves information about the tokens that were issued by this user.
   * @returns An array of objects containing token information including public key, name, symbol, decimals, max supply, freeze status, and extra metadata
   * @throws {SparkRequestError} If the token metadata cannot be retrieved
   */
  public async getIssuerTokensMetadata(): Promise<IssuerTokenMetadata[]> {
    const issuerPublicKey = await super.getIdentityPublicKey();

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

      const tokenMetadata: IssuerTokenMetadata[] = [];

      for (const metadata of response.tokenMetadata) {
        const bech32mTokenIdentifier = encodeBech32mTokenIdentifier({
          tokenIdentifier: metadata.tokenIdentifier,
          network: this.config.getNetworkType(),
        });

        this.tokenMetadata.set(bech32mTokenIdentifier, metadata);

        tokenMetadata.push({
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
          bech32mTokenIdentifier,
        });
      }
      return tokenMetadata;
    } catch (error) {
      throw new SparkRequestError("Failed to fetch token metadata", { error });
    }
  }

  /**
   * Retrieves the bech32m encoded token identifier for the issuer's token.
   * @deprecated Use getIssuerTokenIdentifiers() instead. This method will be removed in a future version.
   * @returns The bech32m encoded token identifier for the issuer's token
   * @throws {SparkRequestError} If the token identifier cannot be retrieved
   * @throws {SparkValidationError} If multiple tokens are found for this issuer
   */
  public async getIssuerTokenIdentifier(): Promise<Bech32mTokenIdentifier> {
    const tokensMetadata = await this.getIssuerTokensMetadata();

    if (tokensMetadata.length > 1) {
      throw new SparkValidationError(
        "Multiple tokens found. Use getIssuerTokenIdentifiers() instead.",
        {
          method: "getIssuerTokenIdentifier",
          availableTokens: tokensMetadata.map((t) => ({
            tokenName: t.tokenName,
            tokenTicker: t.tokenTicker,
            bech32mTokenIdentifier: encodeBech32mTokenIdentifier({
              tokenIdentifier: t.rawTokenIdentifier,
              network: this.config.getNetworkType(),
            }),
          })),
        },
      );
    }

    if (tokensMetadata.length === 0) {
      throw new SparkValidationError("No tokens found. Create a token first.");
    }

    return tokensMetadata[0].bech32mTokenIdentifier;
  }

  /**
   * Retrieves the bech32m encoded token identifier for the issuer's token.
   * @returns The bech32m encoded token identifier for the issuer's token
   * @throws {SparkRequestError} If the token identifier cannot be retrieved
   */
  public async getIssuerTokenIdentifiers(): Promise<Bech32mTokenIdentifier[]> {
    const tokensMetadata = await this.getIssuerTokensMetadata();

    return tokensMetadata.map((metadata) => metadata.bech32mTokenIdentifier);
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
   * @param params.returnIdentifierForCreate - (Optional) Whether to return the token identifier in addition to the transaction hash. Defaults to <code>false</code>.
   * @returns The transaction hash of the announcement or TokenCreationDetails if `returnIdentifierForCreate` is true.
   * @throws {SparkValidationError} If `decimals` is not a safe integer or other validation fails.
   * @throws {SparkRequestError} If the announcement transaction cannot be broadcast.
   */
  public async createToken(params: {
    tokenName: string;
    tokenTicker: string;
    decimals: number;
    isFreezable: boolean;
    maxSupply?: bigint;
    extraMetadata?: Uint8Array;
    returnIdentifierForCreate?: false;
  }): Promise<string>;

  public async createToken(params: {
    tokenName: string;
    tokenTicker: string;
    decimals: number;
    isFreezable: boolean;
    maxSupply?: bigint;
    extraMetadata?: Uint8Array;
    returnIdentifierForCreate: true;
  }): Promise<TokenCreationDetails>;

  public async createToken({
    tokenName,
    tokenTicker,
    decimals,
    isFreezable,
    maxSupply = 0n,
    extraMetadata,
    returnIdentifierForCreate = false,
  }: {
    tokenName: string;
    tokenTicker: string;
    decimals: number;
    isFreezable: boolean;
    maxSupply?: bigint;
    extraMetadata?: Uint8Array;
    returnIdentifierForCreate?: boolean;
  }): Promise<string | TokenCreationDetails> {
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

      const { finalTokenTransactionHash, tokenIdentifier } =
        await this.issuerTokenTransactionService.broadcastTokenTransactionDetailed(
          tokenTransaction,
        );
      const txHash = bytesToHex(finalTokenTransactionHash);

      if (returnIdentifierForCreate) {
        if (!tokenIdentifier) {
          throw new SparkRequestError(
            "Server response missing expected field: tokenIdentifier",
            {
              operation: "broadcast_transaction",
              field: "tokenIdentifier",
            },
          );
        }
        const bech32mTokenIdentifier = encodeBech32mTokenIdentifier({
          tokenIdentifier: tokenIdentifier,
          network: this.config.getNetworkType(),
        });
        return {
          tokenIdentifier: bech32mTokenIdentifier,
          transactionHash: txHash,
        };
      }
      return txHash;
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

      const broadcastResponse =
        await this.issuerTokenTransactionService.broadcastTokenTransactionV3Detailed(
          partialTokenTransaction,
        );
      const finalHash = await hashFinalTokenTransaction(
        broadcastResponse.finalTokenTransaction!,
      );
      const finalTransactionHash = bytesToHex(finalHash);

      if (returnIdentifierForCreate) {
        if (!broadcastResponse.tokenIdentifier) {
          throw new SparkRequestError(
            "Server response missing expected field: tokenIdentifier",
            {
              operation: "broadcast_transaction",
              field: "tokenIdentifier",
            },
          );
        }
        const tokenIdentifier = encodeBech32mTokenIdentifier({
          tokenIdentifier: broadcastResponse.tokenIdentifier!,
          network: this.config.getNetworkType(),
        });
        return {
          tokenIdentifier,
          transactionHash: finalTransactionHash,
        };
      }

      return finalTransactionHash;
    }
  }

  /**
   * @deprecated Use mintTokens({ tokenAmount, tokenIdentifier }) instead. This method will be removed in a future version.
   * @param amount - The amount of tokens to mint
   * @returns The transaction ID of the mint operation
   * @throws {SparkValidationError} If multiple tokens are found for this issuer
   * @throws {SparkValidationError} If no tokens are found for this issuer
   */
  public async mintTokens(amount: bigint): Promise<string>;

  /**
   * Mints new tokens
   * @param params - Object containing token minting parameters.
   * @param params.tokenAmount - The amount of tokens to mint
   * @param params.tokenIdentifier - The bech32m encoded token identifier
   * @returns The transaction ID of the mint operation
   * @throws {SparkValidationError} If no tokens are found for this issuer
   */
  public async mintTokens({
    tokenAmount,
    tokenIdentifier,
  }: {
    tokenAmount: bigint;
    tokenIdentifier?: Bech32mTokenIdentifier;
  }): Promise<string>;

  public async mintTokens(
    tokenAmountOrParams:
      | bigint
      | {
          tokenAmount: bigint;
          tokenIdentifier?: Bech32mTokenIdentifier;
        },
  ): Promise<string> {
    let tokenAmount: bigint;
    let bech32mTokenIdentifier: Bech32mTokenIdentifier | undefined;

    if (typeof tokenAmountOrParams === "bigint") {
      tokenAmount = tokenAmountOrParams;
      bech32mTokenIdentifier = undefined;
    } else {
      tokenAmount = tokenAmountOrParams.tokenAmount;
      bech32mTokenIdentifier = tokenAmountOrParams.tokenIdentifier;
    }

    const issuerTokenPublicKey = await super.getIdentityPublicKey();
    const issuerTokenPublicKeyBytes = hexToBytes(issuerTokenPublicKey);

    const tokensMetadata = await this.getIssuerTokensMetadata();
    if (bech32mTokenIdentifier === undefined) {
      if (tokensMetadata.length > 1) {
        throw new SparkValidationError(
          "Multiple tokens found. Please use mintTokens({ tokenAmount, tokenIdentifier }) instead.",
          {
            field: "tokenIdentifier",
            availableTokens: tokensMetadata.map((t) => ({
              tokenName: t.tokenName,
              tokenTicker: t.tokenTicker,
              bech32mTokenIdentifier: encodeBech32mTokenIdentifier({
                tokenIdentifier: t.rawTokenIdentifier,
                network: this.config.getNetworkType(),
              }),
            })),
          },
        );
      }

      const encodedTokenIdentifier = encodeBech32mTokenIdentifier({
        tokenIdentifier: tokensMetadata[0].rawTokenIdentifier,
        network: this.config.getNetworkType(),
      });
      bech32mTokenIdentifier = encodedTokenIdentifier;
    }

    const rawTokenIdentifier = decodeBech32mTokenIdentifier(
      bech32mTokenIdentifier,
      this.config.getNetworkType(),
    ).tokenIdentifier;

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
   * @deprecated Use burnTokens({ tokenAmount, tokenIdentifier, selectedOutputs }) instead. This method will be removed in a future version.
   * @param tokenAmount - The amount of tokens to burn
   * @param selectedOutputs - Optional array of outputs to use for the burn operation
   * @returns The transaction ID of the burn operation
   * @throws {SparkValidationError} If multiple tokens are found for this issuer
   * @throws {SparkValidationError} If no tokens are found for this issuer
   */
  public async burnTokens(
    tokenAmount: bigint,
    selectedOutputs?: OutputWithPreviousTransactionData[],
  ): Promise<string>;

  /**
   * Burns issuer's tokens
   * @param params - Object containing token burning parameters.
   * @param params.tokenAmount - The amount of tokens to burn
   * @param params.tokenIdentifier - The bech32m encoded token identifier
   * @param params.selectedOutputs - Optional array of outputs to use for the burn operation
   * @returns The transaction ID of the burn operation
   */
  public async burnTokens({
    tokenAmount,
    tokenIdentifier,
    selectedOutputs,
  }: {
    tokenAmount: bigint;
    tokenIdentifier?: Bech32mTokenIdentifier;
    selectedOutputs?: OutputWithPreviousTransactionData[];
  }): Promise<string>;

  public async burnTokens(
    tokenAmountOrParams:
      | bigint
      | {
          tokenAmount: bigint;
          tokenIdentifier?: Bech32mTokenIdentifier;
          selectedOutputs?: OutputWithPreviousTransactionData[];
        },
    selectedOutputs?: OutputWithPreviousTransactionData[],
  ): Promise<string> {
    let bech32mTokenIdentifier: Bech32mTokenIdentifier;
    let tokenAmount: bigint;
    let outputs: OutputWithPreviousTransactionData[] | undefined;

    if (typeof tokenAmountOrParams === "bigint") {
      tokenAmount = tokenAmountOrParams;
      outputs = selectedOutputs;

      const tokenIdentifiers = await this.getIssuerTokenIdentifiers();
      if (tokenIdentifiers.length > 1) {
        throw new SparkValidationError(
          "Multiple tokens found. Use burnTokens({ tokenIdentifier, tokenAmount, selectedOutputs }) to specify which token to burn.",
          {
            field: "tokenIdentifier",
            availableTokens: tokenIdentifiers,
          },
        );
      }
      if (tokenIdentifiers.length === 0) {
        throw new SparkValidationError(
          "No tokens found. Create a token first.",
        );
      }
      bech32mTokenIdentifier = tokenIdentifiers[0];
    } else {
      tokenAmount = tokenAmountOrParams.tokenAmount;
      outputs = tokenAmountOrParams.selectedOutputs;

      if (tokenAmountOrParams.tokenIdentifier) {
        const tokenIdentifiers = await this.getIssuerTokenIdentifiers();
        const tokenIdentifier = tokenIdentifiers.find(
          (identifier) => identifier === tokenAmountOrParams.tokenIdentifier,
        );
        if (!tokenIdentifier) {
          throw new SparkValidationError("Token not found for this issuer", {
            field: "tokenIdentifier",
            value: tokenAmountOrParams.tokenIdentifier,
          });
        }
        bech32mTokenIdentifier = tokenAmountOrParams.tokenIdentifier;
      } else {
        const tokenIdentifiers = await this.getIssuerTokenIdentifiers();
        if (tokenIdentifiers.length === 0) {
          throw new SparkValidationError(
            "No tokens found. Create a token first.",
          );
        }
        if (tokenIdentifiers.length > 1) {
          throw new SparkValidationError(
            "Multiple tokens found. Please specify tokenIdentifier in parameters.",
            {
              field: "tokenIdentifier",
              availableTokens: tokenIdentifiers,
            },
          );
        }
        bech32mTokenIdentifier = tokenIdentifiers[0];
      }
    }

    const burnAddress = encodeSparkAddress({
      identityPublicKey: BURN_ADDRESS,
      network: this.config.getNetworkType(),
    });

    return await this.transferTokens({
      tokenIdentifier: bech32mTokenIdentifier,
      tokenAmount,
      receiverSparkAddress: burnAddress,
      selectedOutputs: outputs,
    });
  }

  /**
   * Freezes tokens associated with a specific Spark address.
   * @deprecated Use freezeToken({ tokenIdentifier, sparkAddress }) instead. This method will be removed in a future version.
   * @param sparkAddress - The Spark address whose tokens should be frozen
   * @returns An object containing the IDs of impacted outputs and the total amount of frozen tokens
   * @throws {SparkValidationError} If multiple tokens are found for this issuer
   * @throws {SparkValidationError} If no tokens are found for this issuer
   */
  public async freezeTokens(
    sparkAddress: string,
  ): Promise<{ impactedOutputIds: string[]; impactedTokenAmount: bigint }>;

  /**
   * Freezes tokens associated with a specific Spark address.
   * @param params - Object containing token freezing parameters.
   * @param params.tokenIdentifier - The bech32m encoded token identifier
   * @param params.sparkAddress - The Spark address whose tokens should be frozen
   * @returns An object containing the IDs of impacted outputs and the total amount of frozen tokens
   */
  public async freezeTokens({
    tokenIdentifier,
    sparkAddress,
  }: {
    tokenIdentifier: Bech32mTokenIdentifier;
    sparkAddress: string;
  }): Promise<{ impactedOutputIds: string[]; impactedTokenAmount: bigint }>;

  public async freezeTokens(
    sparkAddressOrParams:
      | string
      | {
          tokenIdentifier: Bech32mTokenIdentifier;
          sparkAddress: string;
        },
  ): Promise<{ impactedOutputIds: string[]; impactedTokenAmount: bigint }> {
    let bech32mTokenIdentifier: Bech32mTokenIdentifier | undefined;
    let sparkAddress: string;

    if (typeof sparkAddressOrParams === "string") {
      sparkAddress = sparkAddressOrParams;
      bech32mTokenIdentifier = undefined;
    } else {
      sparkAddress = sparkAddressOrParams.sparkAddress;
      bech32mTokenIdentifier = sparkAddressOrParams.tokenIdentifier;
    }

    const decodedOwnerPubkey = decodeSparkAddress(
      sparkAddress,
      this.config.getNetworkType(),
    );

    if (bech32mTokenIdentifier === undefined) {
      const tokenIdentifiers = await this.getIssuerTokenIdentifiers();
      if (tokenIdentifiers.length === 0) {
        throw new SparkValidationError(
          "No tokens found. Create a token first.",
        );
      }
      if (tokenIdentifiers.length > 1) {
        throw new SparkValidationError(
          "Multiple tokens found. Use freezeTokens({ tokenIdentifier, sparkAddress }) instead.",
          {
            field: "tokenIdentifier",
            availableTokens: tokenIdentifiers,
          },
        );
      }
      bech32mTokenIdentifier = tokenIdentifiers[0];
    }

    const rawTokenIdentifier = decodeBech32mTokenIdentifier(
      bech32mTokenIdentifier,
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
   * @deprecated Use unfreezeToken({ tokenIdentifier, sparkAddress }) instead. This method will be removed in a future version.
   * @param sparkAddress - The Spark address whose tokens should be unfrozen
   * @returns An object containing the IDs of impacted outputs and the total amount of unfrozen tokens
   * @throws {SparkValidationError} If multiple tokens are found for this issuer
   * @throws {SparkValidationError} If no tokens are found for this issuer
   */
  public async unfreezeTokens(
    sparkAddress: string,
  ): Promise<{ impactedOutputIds: string[]; impactedTokenAmount: bigint }>;

  /**
   * Unfreezes previously frozen tokens associated with a specific Spark address.
   * @param params - Object containing token unfreezing parameters.
   * @param params.tokenIdentifier - The bech32m encoded token identifier
   * @param params.sparkAddress - The Spark address whose tokens should be unfrozen
   * @returns An object containing the IDs of impacted outputs and the total amount of unfrozen tokens
   * @throws {SparkValidationError} If multiple tokens are found for this issuer
   * @throws {SparkValidationError} If no tokens are found for this issuer
   */
  public async unfreezeTokens({
    tokenIdentifier,
    sparkAddress,
  }: {
    tokenIdentifier: Bech32mTokenIdentifier;
    sparkAddress: string;
  }): Promise<{ impactedOutputIds: string[]; impactedTokenAmount: bigint }>;

  public async unfreezeTokens(
    sparkAddressOrParams:
      | string
      | {
          tokenIdentifier: Bech32mTokenIdentifier;
          sparkAddress: string;
        },
  ): Promise<{ impactedOutputIds: string[]; impactedTokenAmount: bigint }> {
    let bech32mTokenIdentifier: Bech32mTokenIdentifier | undefined;
    let sparkAddress: string;

    if (typeof sparkAddressOrParams === "string") {
      sparkAddress = sparkAddressOrParams;
      bech32mTokenIdentifier = undefined;
    } else {
      sparkAddress = sparkAddressOrParams.sparkAddress;
      bech32mTokenIdentifier = sparkAddressOrParams.tokenIdentifier;
    }

    if (bech32mTokenIdentifier === undefined) {
      const tokenIdentifiers = await this.getIssuerTokenIdentifiers();
      if (tokenIdentifiers.length > 1) {
        throw new SparkValidationError(
          "Multiple tokens found. Use unfreezeTokens({ tokenIdentifier, sparkAddress }) instead.",
          {
            field: "tokenIdentifier",
            availableTokens: tokenIdentifiers,
          },
        );
      }
      bech32mTokenIdentifier = tokenIdentifiers[0];
    }

    const decodedOwnerPubkey = decodeSparkAddress(
      sparkAddress,
      this.config.getNetworkType(),
    );

    const rawTokenIdentifier = decodeBech32mTokenIdentifier(
      bech32mTokenIdentifier,
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
  "getIssuerTokenBalances",
  "getIssuerTokenMetadata",
  "getIssuerTokensMetadata",
  "getIssuerTokenIdentifier",
  "getIssuerTokenIdentifiers",
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
