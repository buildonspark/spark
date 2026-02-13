import { bytesToHex, bytesToNumberBE, hexToBytes } from "@noble/curves/utils";
import { validateMnemonic } from "@scure/bip39";
import { wordlist } from "@scure/bip39/wordlists/english";
import {
  Bech32mTokenIdentifier,
  decodeBech32mTokenIdentifier,
  decodeSparkAddress,
  DefaultSparkSigner,
  encodeBech32mTokenIdentifier,
  SparkRequestError,
  SparkSigner,
  SparkValidationError,
  TokenBalanceMap,
} from "../index-shared.js";
import {
  type DeepPartial,
  DepositAddressQueryResult,
  Direction,
  HashVariant,
  InvoiceResponse,
  Order,
  Transfer,
  TransferFilter,
  TransferType,
  TreeNodeStatus,
} from "../proto/spark.js";
import {
  OutputWithPreviousTransactionData,
  QueryTokenTransactionsResponse,
  TokenOutputStatus,
  TokenTransactionWithStatus,
} from "../proto/spark_token.js";
import {
  AuthMode,
  ConfigOptions,
  ConnectionManager,
  WalletConfigService,
} from "../services/index.js";

// ── Constants ─────────────────────────────────────────────────────

const QUERY_TOKEN_OUTPUTS_PAGE_SIZE = 100;

const DEFAULT_TRANSFER_TYPES = [
  TransferType.TRANSFER,
  TransferType.PREIMAGE_SWAP,
  TransferType.COOPERATIVE_EXIT,
  TransferType.UTXO_SWAP,
];

// ── Param Types ───────────────────────────────────────────────────

export type QueryTransfersParams = {
  sparkAddress: string;
  limit?: number;
  offset?: number;
  types?: TransferType[];
  createdAfter?: Date;
  createdBefore?: Date;
};

export type QueryDepositAddressesParams = {
  sparkAddress: string;
  limit?: number;
  offset?: number;
};

export type GetUtxosParams = {
  depositAddress: string;
  limit?: number;
  offset?: number;
  excludeClaimed?: boolean;
};

export type QuerySparkInvoicesParams = {
  invoices: string[];
  limit?: number;
  offset?: number;
};

export type QueryTokenTransactionsParams = {
  sparkAddresses?: string[];
  issuerPublicKeys?: string[];
  tokenIdentifiers?: string[];
  outputIds?: string[];
  pageSize?: number;
  cursor?: string;
  direction?: "NEXT" | "PREVIOUS";
};

// ── Client ────────────────────────────────────────────────────────

export abstract class SparkReadonlyClient {
  protected readonly connectionManager: ConnectionManager;
  protected readonly config: WalletConfigService;

  constructor(
    config: ConfigOptions | undefined,
    signer: SparkSigner,
    authMode: AuthMode = "identity",
  ) {
    this.config = new WalletConfigService(config, signer);
    this.connectionManager = this.buildConnectionManager(this.config, authMode);
  }

  static createPublic<T extends SparkReadonlyClient>(
    this: new (
      config: ConfigOptions | undefined,
      signer: SparkSigner,
      authMode: AuthMode,
    ) => T,
    config?: ConfigOptions,
  ): T {
    return new this(config, new DefaultSparkSigner(), "none");
  }

  static async createWithMasterKey<T extends SparkReadonlyClient>(
    this: new (
      config: ConfigOptions | undefined,
      signer: SparkSigner,
      authMode: AuthMode,
    ) => T,
    config: ConfigOptions | undefined,
    mnemonicOrSeed: Uint8Array | string,
    accountNumber?: number,
  ): Promise<T> {
    const signer = new DefaultSparkSigner();

    let seed: Uint8Array;
    if (typeof mnemonicOrSeed === "string") {
      if (validateMnemonic(mnemonicOrSeed, wordlist)) {
        seed = await signer.mnemonicToSeed(mnemonicOrSeed);
      } else {
        seed = hexToBytes(mnemonicOrSeed);
      }
    } else {
      seed = mnemonicOrSeed;
    }

    if (accountNumber === undefined) {
      accountNumber = config?.network === "REGTEST" ? 0 : 1;
    }

    await signer.createSparkWalletFromSeed(seed, accountNumber);
    return new this(config, signer, "identity");
  }

  static createWithSigner<T extends SparkReadonlyClient>(
    this: new (
      config: ConfigOptions | undefined,
      signer: SparkSigner,
      authMode: AuthMode,
    ) => T,
    config: ConfigOptions | undefined,
    signer: SparkSigner,
  ): T {
    return new this(config, signer, "identity");
  }

  // ── Balances ────────────────────────────────────────────────────

  /** Returns the total available (non-pending) sats balance. Auto-paginates internally. */
  public async getAvailableBalance(sparkAddress: string): Promise<bigint> {
    const identityPublicKey = this.resolveIdentityPublicKey(sparkAddress);
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    try {
      let availableBalance = 0n;
      let offset = 0;
      const seenOffsets = new Set<number>();

      while (offset >= 0) {
        if (seenOffsets.has(offset)) {
          throw new Error(
            "Detected repeated offset while paginating available balance",
          );
        }
        seenOffsets.add(offset);

        const result = await sparkClient.query_nodes({
          source: {
            $case: "ownerIdentityPubkey",
            ownerIdentityPubkey: identityPublicKey,
          },
          includeParents: false,
          network: this.config.getNetworkProto(),
          statuses: [TreeNodeStatus.TREE_NODE_STATUS_AVAILABLE],
          offset,
          limit: 100,
        });

        availableBalance += Object.values(result.nodes).reduce(
          (acc, node) => acc + BigInt(node.value),
          0n,
        );
        offset = result.offset;
      }

      return availableBalance;
    } catch (error) {
      throw new SparkRequestError("Failed to get available balance", {
        operation: "query_nodes",
        error,
      });
    }
  }

  /** Returns token balances grouped by token identifier. Auto-paginates internally. */
  public async getTokenBalance(
    sparkAddress: string,
    tokenIdentifiers?: string[],
  ): Promise<TokenBalanceMap> {
    const typedTokenIdentifiers = tokenIdentifiers?.map((id) =>
      this.decodeTokenIdentifier(id),
    );

    const identityPublicKey = this.resolveIdentityPublicKey(sparkAddress);
    const tokenClient = await this.connectionManager.createSparkTokenClient(
      this.config.getCoordinatorAddress(),
    );

    try {
      // 1. Paginate through all token outputs
      const allOutputs = await this.fetchAllTokenOutputs(
        tokenClient,
        identityPublicKey,
        typedTokenIdentifiers,
      );

      // No outputs means no balances and avoids an unnecessary metadata query.
      if (allOutputs.length === 0) {
        return new Map();
      }

      // 2. Group outputs by token identifier and sum available vs pending
      const availableSums = new Map<string, bigint>();
      const pendingSums = new Map<string, bigint>();
      const uniqueTokenIdentifiers: Uint8Array[] = [];
      const seenIdentifiers = new Set<string>();

      for (const output of allOutputs) {
        const tokenId = output.output?.tokenIdentifier;
        const tokenAmount = output.output?.tokenAmount;
        if (!tokenId || !tokenAmount) continue;

        const tokenIdHex = bytesToHex(tokenId);
        if (!seenIdentifiers.has(tokenIdHex)) {
          seenIdentifiers.add(tokenIdHex);
          uniqueTokenIdentifiers.push(tokenId);
        }

        const amount = bytesToNumberBE(tokenAmount);
        if (
          output.output?.status ===
          TokenOutputStatus.TOKEN_OUTPUT_STATUS_AVAILABLE
        ) {
          availableSums.set(
            tokenIdHex,
            (availableSums.get(tokenIdHex) ?? 0n) + amount,
          );
        } else if (
          output.output?.status ===
          TokenOutputStatus.TOKEN_OUTPUT_STATUS_PENDING_OUTBOUND
        ) {
          pendingSums.set(
            tokenIdHex,
            (pendingSums.get(tokenIdHex) ?? 0n) + amount,
          );
        }
      }

      // 3. Fetch metadata for all unique token identifiers
      const tokenMetadataResponse = await tokenClient.query_token_metadata({
        tokenIdentifiers: uniqueTokenIdentifiers,
      });

      // 4. Build the TokenBalanceMap
      const tokenBalanceMap: TokenBalanceMap = new Map();
      for (const metadata of tokenMetadataResponse.tokenMetadata) {
        const tokenIdHex = bytesToHex(metadata.tokenIdentifier);
        const humanReadableId = encodeBech32mTokenIdentifier({
          tokenIdentifier: metadata.tokenIdentifier,
          network: this.config.getNetworkType(),
        });

        const available = availableSums.get(tokenIdHex) ?? 0n;
        const pending = pendingSums.get(tokenIdHex) ?? 0n;

        tokenBalanceMap.set(humanReadableId, {
          ownedBalance: available + pending,
          availableToSendBalance: available,
          tokenMetadata: {
            tokenPublicKey: bytesToHex(metadata.issuerPublicKey),
            rawTokenIdentifier: metadata.tokenIdentifier,
            tokenName: metadata.tokenName,
            tokenTicker: metadata.tokenTicker,
            decimals: metadata.decimals,
            maxSupply: bytesToNumberBE(metadata.maxSupply),
            extraMetadata: metadata.extraMetadata
              ? new Uint8Array(metadata.extraMetadata)
              : undefined,
          },
        });
      }

      return tokenBalanceMap;
    } catch (error) {
      throw new SparkRequestError("Failed to fetch token balance", {
        operation: "query_token_outputs",
        error,
      });
    }
  }

  // ── Transfers ───────────────────────────────────────────────────

  /** Queries paginated transfers for a spark address. */
  public async getTransfers(params: QueryTransfersParams): Promise<{
    transfers: Transfer[];
    offset: number;
  }> {
    const {
      sparkAddress,
      limit = 20,
      offset = 0,
      types = DEFAULT_TRANSFER_TYPES,
      createdAfter,
      createdBefore,
    } = params;

    this.assertPositiveInteger(limit, "limit");
    this.assertNonNegativeInteger(offset, "offset");

    if (createdAfter && createdBefore) {
      throw new SparkValidationError(
        "createdAfter and createdBefore are mutually exclusive",
      );
    }

    const identityPublicKey = this.resolveIdentityPublicKey(sparkAddress);
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    const filter: DeepPartial<TransferFilter> = {
      participant: {
        $case: "senderOrReceiverIdentityPublicKey" as const,
        senderOrReceiverIdentityPublicKey: identityPublicKey,
      },
      limit,
      offset,
      types,
      network: this.config.getNetworkProto(),
    };

    if (createdAfter) {
      filter.timeFilter = {
        $case: "createdAfter" as const,
        createdAfter,
      };
    } else if (createdBefore) {
      filter.timeFilter = {
        $case: "createdBefore" as const,
        createdBefore,
      };
    }

    try {
      const result = await sparkClient.query_all_transfers(filter);
      return { transfers: result.transfers, offset: result.offset };
    } catch (error) {
      throw new SparkRequestError("Failed to query transfers", {
        operation: "query_all_transfers",
        error,
      });
    }
  }

  /** Looks up specific transfers by their IDs. */
  public async getTransfersByIds(transferIds: string[]): Promise<Transfer[]> {
    this.assertNonEmptyArray(transferIds, "transferIds");

    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    try {
      const result = await sparkClient.query_all_transfers({
        transferIds,
        network: this.config.getNetworkProto(),
      });
      return result.transfers;
    } catch (error) {
      throw new SparkRequestError("Failed to query transfers by IDs", {
        operation: "query_all_transfers",
        error,
      });
    }
  }

  /** Queries pending inbound transfers for a spark address. */
  public async getPendingTransfers(sparkAddress: string): Promise<Transfer[]> {
    const identityPublicKey = this.resolveIdentityPublicKey(sparkAddress);
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    try {
      const result = await sparkClient.query_pending_transfers({
        participant: {
          $case: "receiverIdentityPublicKey" as const,
          receiverIdentityPublicKey: identityPublicKey,
        },
        network: this.config.getNetworkProto(),
      });
      return result.transfers;
    } catch (error) {
      throw new SparkRequestError("Failed to query pending transfers", {
        operation: "query_pending_transfers",
        error,
      });
    }
  }

  // ── Deposit Addresses ───────────────────────────────────────────

  /** Queries paginated unused (single-use) deposit addresses. */
  public async getUnusedDepositAddresses(
    params: QueryDepositAddressesParams,
  ): Promise<{
    depositAddresses: DepositAddressQueryResult[];
    offset: number;
  }> {
    const { sparkAddress, limit = 100, offset = 0 } = params;
    this.assertPositiveInteger(limit, "limit");
    this.assertNonNegativeInteger(offset, "offset");

    const identityPublicKey = this.resolveIdentityPublicKey(sparkAddress);
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    try {
      const result = await sparkClient.query_unused_deposit_addresses({
        identityPublicKey,
        network: this.config.getNetworkProto(),
        limit,
        offset,
      });
      return {
        depositAddresses: result.depositAddresses,
        offset: result.offset,
      };
    } catch (error) {
      throw new SparkRequestError("Failed to query unused deposit addresses", {
        operation: "query_unused_deposit_addresses",
        error,
      });
    }
  }

  /** Returns all static deposit addresses for a spark address. */
  public async getStaticDepositAddresses(
    sparkAddress: string,
  ): Promise<DepositAddressQueryResult[]> {
    const identityPublicKey = this.resolveIdentityPublicKey(sparkAddress);
    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    try {
      const result = await sparkClient.query_static_deposit_addresses({
        identityPublicKey,
        network: this.config.getNetworkProto(),
        hashVariant: HashVariant.HASH_VARIANT_V2,
      });
      return result.depositAddresses;
    } catch (error) {
      throw new SparkRequestError("Failed to query static deposit addresses", {
        operation: "query_static_deposit_addresses",
        error,
      });
    }
  }

  // ── UTXOs ───────────────────────────────────────────────────────

  /** Queries paginated confirmed UTXOs for a deposit address. */
  public async getUtxosForDepositAddress(params: GetUtxosParams): Promise<{
    utxos: { txid: string; vout: number }[];
    offset: number;
  }> {
    const {
      depositAddress,
      limit = 100,
      offset = 0,
      excludeClaimed = false,
    } = params;
    this.assertPositiveInteger(limit, "limit");
    this.assertNonNegativeInteger(offset, "offset");

    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    try {
      const result = await sparkClient.get_utxos_for_address({
        address: depositAddress,
        network: this.config.getNetworkProto(),
        limit,
        offset,
        excludeClaimed,
      });
      return {
        utxos: result.utxos.map((utxo) => ({
          txid: bytesToHex(utxo.txid),
          vout: utxo.vout,
        })),
        offset: result.offset,
      };
    } catch (error) {
      throw new SparkRequestError("Failed to get UTXOs for deposit address", {
        operation: "get_utxos_for_address",
        error,
      });
    }
  }

  // ── Spark Invoices ──────────────────────────────────────────────

  /** Queries paginated spark invoice statuses. */
  public async getSparkInvoices(params: QuerySparkInvoicesParams): Promise<{
    invoiceStatuses: InvoiceResponse[];
    offset: number;
  }> {
    const { invoices, limit = 20, offset = 0 } = params;
    this.assertNonEmptyArray(invoices, "invoices");
    this.assertPositiveInteger(limit, "limit");
    this.assertNonNegativeInteger(offset, "offset");

    const sparkClient = await this.connectionManager.createSparkClient(
      this.config.getCoordinatorAddress(),
    );

    try {
      const result = await sparkClient.query_spark_invoices({
        invoice: invoices,
        limit,
        offset,
      });
      return {
        invoiceStatuses: result.invoiceStatuses,
        offset: result.offset,
      };
    } catch (error) {
      throw new SparkRequestError("Failed to query spark invoices", {
        operation: "query_spark_invoices",
        error,
      });
    }
  }

  // ── Token Transactions ──────────────────────────────────────────

  /** Queries token transactions with optional filters and cursor-based pagination. */
  public async getTokenTransactions(
    params: QueryTokenTransactionsParams = {},
  ): Promise<{
    transactions: TokenTransactionWithStatus[];
    pageResponse: QueryTokenTransactionsResponse["pageResponse"];
  }> {
    const {
      sparkAddresses,
      issuerPublicKeys,
      tokenIdentifiers,
      outputIds,
      pageSize = 50,
      cursor,
      direction = "NEXT",
    } = params;

    this.assertPositiveInteger(pageSize, "pageSize");

    const ownerPublicKeys = sparkAddresses?.map((addr) =>
      this.resolveIdentityPublicKey(addr),
    );

    const typedTokenIdentifiers = tokenIdentifiers?.map((id) =>
      this.decodeTokenIdentifier(id),
    );

    const tokenClient = await this.connectionManager.createSparkTokenClient(
      this.config.getCoordinatorAddress(),
    );

    try {
      const result = await tokenClient.query_token_transactions({
        queryType: {
          $case: "byFilters" as const,
          byFilters: {
            outputIds: outputIds ?? [],
            ownerPublicKeys: ownerPublicKeys ?? [],
            issuerPublicKeys: issuerPublicKeys?.map(hexToBytes) ?? [],
            tokenIdentifiers: typedTokenIdentifiers ?? [],
            pageRequest: {
              unsafePageSize: 0,
              pageSize,
              cursor: cursor ?? "",
              direction:
                direction === "PREVIOUS" ? Direction.PREVIOUS : Direction.NEXT,
            },
          },
        },
        // Legacy fields — required by the proto but unused with byFilters
        outputIds: [],
        ownerPublicKeys: [],
        issuerPublicKeys: [],
        tokenIdentifiers: [],
        tokenTransactionHashes: [],
        order: Order.UNRECOGNIZED,
        limit: 0,
        offset: 0,
      });
      return {
        transactions: result.tokenTransactionsWithStatus,
        pageResponse: result.pageResponse,
      };
    } catch (error) {
      throw new SparkRequestError("Failed to query token transactions", {
        operation: "query_token_transactions",
        error,
      });
    }
  }

  // ── Helpers ─────────────────────────────────────────────────────

  /** Decodes a spark address and returns the raw identity public key bytes. */
  private resolveIdentityPublicKey(sparkAddress: string): Uint8Array {
    const addressData = decodeSparkAddress(
      sparkAddress,
      this.config.getNetworkType(),
    );
    return hexToBytes(addressData.identityPublicKey);
  }

  /** Decodes a bech32m token identifier string to raw bytes. */
  private decodeTokenIdentifier(identifier: string): Uint8Array {
    try {
      const { tokenIdentifier } = decodeBech32mTokenIdentifier(
        identifier as Bech32mTokenIdentifier,
        this.config.getNetworkType(),
      );
      return tokenIdentifier;
    } catch (error) {
      throw new SparkValidationError("Invalid token identifier", {
        field: "tokenIdentifier",
        value: identifier,
        error,
      });
    }
  }

  /** Auto-paginates through all token outputs for a given owner. */
  private async fetchAllTokenOutputs(
    tokenClient: Awaited<
      ReturnType<ConnectionManager["createSparkTokenClient"]>
    >,
    ownerPublicKey: Uint8Array,
    tokenIdentifiers?: Uint8Array[],
  ): Promise<OutputWithPreviousTransactionData[]> {
    const allOutputs: OutputWithPreviousTransactionData[] = [];
    let cursor: string | undefined;
    const seenCursors = new Set<string>();

    do {
      const result = await tokenClient.query_token_outputs({
        ownerPublicKeys: [ownerPublicKey],
        tokenIdentifiers,
        network: this.config.getNetworkProto(),
        pageRequest: {
          pageSize: QUERY_TOKEN_OUTPUTS_PAGE_SIZE,
          cursor,
          direction: Direction.NEXT,
        },
      });

      if (Array.isArray(result.outputsWithPreviousTransactionData)) {
        allOutputs.push(...result.outputsWithPreviousTransactionData);
      }

      if (result.pageResponse?.hasNextPage) {
        const nextCursor = result.pageResponse.nextCursor;
        if (seenCursors.has(nextCursor)) {
          throw new Error(
            "Detected repeated cursor while paginating token outputs",
          );
        }
        seenCursors.add(nextCursor);
        cursor = nextCursor;
      } else {
        break;
      }
    } while (cursor);

    return allOutputs;
  }

  private assertPositiveInteger(value: number, field: string): void {
    if (!Number.isInteger(value) || value < 1) {
      throw new SparkValidationError(`${field} must be a positive integer`, {
        field,
        value,
        expected: "positive integer",
      });
    }
  }

  private assertNonNegativeInteger(value: number, field: string): void {
    if (!Number.isInteger(value) || value < 0) {
      throw new SparkValidationError(
        `${field} must be a non-negative integer`,
        {
          field,
          value,
          expected: "non-negative integer",
        },
      );
    }
  }

  private assertNonEmptyArray<T>(value: T[], field: string): void {
    if (!Array.isArray(value) || value.length === 0) {
      throw new SparkValidationError(`${field} must be a non-empty array`, {
        field,
        value,
        expected: "non-empty array",
      });
    }
  }

  // ── Abstract ────────────────────────────────────────────────────

  protected abstract buildConnectionManager(
    config: WalletConfigService,
    authMode: AuthMode,
  ): ConnectionManager;
}
