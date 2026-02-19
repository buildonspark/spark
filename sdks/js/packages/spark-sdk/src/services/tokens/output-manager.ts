import { Mutex } from "async-mutex";
import { SparkValidationError } from "../../errors/types.js";
import {
  OutputWithPreviousTransactionData,
  TokenOutputStatus,
} from "../../proto/spark_token.js";
import { TokenOutputsMap } from "../../spark-wallet/types.js";
import { Bech32mTokenIdentifier } from "../../utils/token-identifier.js";

export type TokenOutputLock = {
  lockedAt: number;
  operationId?: string;
};

export type TokenOutputAcquireRequest = {
  tokenIdentifier: Bech32mTokenIdentifier;
  selector: (
    outputs: OutputWithPreviousTransactionData[],
    remainingCapacity: number,
  ) => OutputWithPreviousTransactionData[];
};

type AcquireOutputsMode = "single" | "batch";

export class TokenOutputManager {
  private availableOutputs: TokenOutputsMap = new Map();
  // A local lock is created when a transaction is started from the wallet
  // It's purely meant to prevent concurrent transactions from spending the same outputs.
  // Local locks expire after a configurable time (default: 30 seconds), if they're not returned from the server (SO) as pending.
  // This is in the case where the transaction does not get broadcasted to the SO for whatever reason.
  private localPendingMap: Map<string, TokenOutputLock> = new Map();
  // A server lock is created when an output is fetched from the server as pending (query_token_outputs)
  // which removes the local lock.
  private serverPendingMap: TokenOutputsMap = new Map();
  private readonly mutex = new Mutex();
  private readonly lockExpiryMs: number;

  constructor(lockExpiryMs: number = 30000) {
    this.lockExpiryMs = lockExpiryMs;
  }

  /**
   * Sync all outputs from the server
   *
   * @param serverProvidedOutputs - All outputs from the server, grouped by token identifier
   * @param tokenIdentifiers - If provided, only update these tokens (preserving others).
   *                           If omitted or empty, replaces all outputs.
   */
  async setOutputs(
    serverProvidedOutputs: TokenOutputsMap,
    tokenIdentifiers?: Bech32mTokenIdentifier[],
  ): Promise<void> {
    await this.mutex.runExclusive(() => {
      const availableByToken: TokenOutputsMap = new Map();
      const pendingByToken: TokenOutputsMap = new Map();

      for (const [tokenId, outputs] of serverProvidedOutputs) {
        const available: OutputWithPreviousTransactionData[] = [];
        const pending: OutputWithPreviousTransactionData[] = [];

        for (const output of outputs) {
          if (
            output.output?.status ===
            TokenOutputStatus.TOKEN_OUTPUT_STATUS_PENDING_OUTBOUND
          ) {
            pending.push(output);
            if (output.output?.id) {
              // Remove the local lock as the output is now pending on the server
              this.localPendingMap.delete(output.output.id);
            }
          } else if (
            output.output?.status ===
            TokenOutputStatus.TOKEN_OUTPUT_STATUS_AVAILABLE
          ) {
            available.push(output);
          }
        }

        if (available.length > 0) {
          availableByToken.set(tokenId, available);
        }
        if (pending.length > 0) {
          pendingByToken.set(tokenId, pending);
        }
      }

      if (tokenIdentifiers && tokenIdentifiers.length > 0) {
        for (const tokenId of tokenIdentifiers) {
          const available = availableByToken.get(tokenId);
          if (available && available.length > 0) {
            this.availableOutputs.set(tokenId, [...available]);
          } else {
            this.availableOutputs.delete(tokenId);
          }

          const pending = pendingByToken.get(tokenId);
          if (pending && pending.length > 0) {
            this.serverPendingMap.set(tokenId, [...pending]);
          } else {
            this.serverPendingMap.delete(tokenId);
          }
        }
      } else {
        this.availableOutputs = new Map(
          [...availableByToken.entries()].map(([k, v]) => [k, [...v]]),
        );
        this.serverPendingMap = new Map(
          [...pendingByToken.entries()].map(([k, v]) => [k, [...v]]),
        );
      }
    });
  }

  /**
   * Get pending outbound outputs for a token.
   */
  async getPendingOutboundOutputs(
    tokenIdentifier: Bech32mTokenIdentifier,
  ): Promise<OutputWithPreviousTransactionData[]> {
    return await this.mutex.runExclusive(() => {
      const serverPending = this.serverPendingMap.get(tokenIdentifier) ?? [];
      const availableForToken =
        this.availableOutputs.get(tokenIdentifier) ?? [];

      const localPendingIds = new Set(this.localPendingMap.keys());
      const localPendingOutputs = availableForToken.filter((output) => {
        const id = output.output?.id;
        return id != null && localPendingIds.has(id);
      });

      return [...serverPending, ...localPendingOutputs];
    });
  }

  /**
   * Get available outputs for a token (including client-locked pending ones)
   */
  async getAvailableOutputs(
    tokenIdentifier: Bech32mTokenIdentifier,
  ): Promise<OutputWithPreviousTransactionData[]> {
    return await this.mutex.runExclusive(() => {
      return [...(this.availableOutputs.get(tokenIdentifier) ?? [])];
    });
  }

  /**
   * Check if outputs map has a token identifier.
   */
  async hasTokenIdentifier(
    tokenIdentifier: Bech32mTokenIdentifier,
  ): Promise<boolean> {
    return await this.mutex.runExclusive(() => {
      return (
        this.availableOutputs.has(tokenIdentifier) ||
        this.serverPendingMap.has(tokenIdentifier)
      );
    });
  }

  /**
   * Get all token identifiers in the map.
   */
  async getTokenIdentifiers(): Promise<Bech32mTokenIdentifier[]> {
    return await this.mutex.runExclusive(() => {
      return this.getAllKeys();
    });
  }

  /**
   * Iterate over entries (snapshot).
   */
  async entries(): Promise<
    [Bech32mTokenIdentifier, OutputWithPreviousTransactionData[]][]
  > {
    return await this.mutex.runExclusive(() => {
      return [...this.availableOutputs.entries()];
    });
  }

  /**
   * Atomically select and lock outputs.
   * Returns the selected outputs
   *
   * @param tokenIdentifier - The token to select from
   * @param selector - Function to select outputs from available (unlocked) outputs
   * @param operationId - name of the operation for debugging purposes
   * @returns outputs that were selected and locked
   */
  async acquireOutputs(
    tokenIdentifier: Bech32mTokenIdentifier,
    selector: (
      outputs: OutputWithPreviousTransactionData[],
    ) => OutputWithPreviousTransactionData[],
    operationId?: string,
  ): Promise<OutputWithPreviousTransactionData[]> {
    return await this.mutex.runExclusive(() => {
      this.cleanupExpiredLocks();

      const selectedByToken = this.acquireOutputsInternal(
        [
          {
            tokenIdentifier,
            selector: (outputs) => selector(outputs),
          },
        ],
        Number.MAX_SAFE_INTEGER,
        operationId,
        "single",
      );
      return selectedByToken.get(tokenIdentifier) ?? [];
    });
  }

  /**
   * Atomically acquires and locks outputs across multiple token identifiers.
   *
   * @param requests - Per-token acquire requests in priority order
   * @param maxTotalOutputs - Maximum number of outputs to acquire across all requests
   * @param operationId - name of the operation for debugging purposes
   */
  async acquireOutputsBatch(
    requests: TokenOutputAcquireRequest[],
    maxTotalOutputs: number,
    operationId?: string,
  ): Promise<TokenOutputsMap> {
    return await this.mutex.runExclusive(() => {
      this.cleanupExpiredLocks();
      return this.acquireOutputsInternal(
        requests,
        maxTotalOutputs,
        operationId,
        "batch",
      );
    });
  }

  /**
   * Lock outputs locally.
   */
  async lockOutputs(
    outputs: OutputWithPreviousTransactionData[],
    operationId?: string,
  ): Promise<void> {
    await this.mutex.runExclusive(() => {
      const now = Date.now();
      for (const output of outputs) {
        const id = output.output!.id!;
        this.localPendingMap.set(id, { lockedAt: now, operationId });
      }
    });
  }

  /**
   * Lock specific outputs by ID
   */
  async lockOutputsByIds(
    outputIds: string[],
    operationId?: string,
  ): Promise<void> {
    await this.mutex.runExclusive(() => {
      const now = Date.now();
      for (const id of outputIds) {
        this.localPendingMap.set(id, { lockedAt: now, operationId });
      }
    });
  }

  /**
   * Check if an output is locked.
   */
  async isLocked(outputId: string): Promise<boolean> {
    return await this.mutex.runExclusive(() => {
      this.cleanupExpiredLocks();
      return this.localPendingMap.has(outputId);
    });
  }

  /**
   * Check if outputs map is empty.
   */
  async isEmpty(): Promise<boolean> {
    return await this.mutex.runExclusive(() => {
      return (
        this.availableOutputs.size === 0 && this.serverPendingMap.size === 0
      );
    });
  }

  /**
   * Get size of outputs map (number of token identifiers).
   */
  async size(): Promise<number> {
    return await this.mutex.runExclusive(() => {
      return this.getAllKeys().length;
    });
  }

  /**
   * Clear all outputs and locks.
   */
  async clear(): Promise<void> {
    await this.mutex.runExclusive(() => {
      this.availableOutputs.clear();
      this.serverPendingMap.clear();
      this.localPendingMap.clear();
    });
  }

  private getUnlockedOutputsInternal(
    tokenIdentifier: Bech32mTokenIdentifier,
  ): OutputWithPreviousTransactionData[] {
    const outputs = this.availableOutputs.get(tokenIdentifier) ?? [];
    return outputs.filter((o) => !this.localPendingMap.has(o.output!.id!));
  }

  private acquireOutputsInternal(
    requests: TokenOutputAcquireRequest[],
    maxTotalOutputs: number,
    operationId: string | undefined,
    mode: AcquireOutputsMode,
  ): TokenOutputsMap {
    if (maxTotalOutputs <= 0 || requests.length === 0) {
      return new Map();
    }

    const selectedByToken: TokenOutputsMap = new Map();
    const selectedIds = new Set<string>();
    let remainingCapacity = maxTotalOutputs;

    for (const request of requests) {
      if (remainingCapacity <= 0) {
        break;
      }

      const available = this.getUnlockedOutputsInternal(
        request.tokenIdentifier,
      );
      const selected = request.selector(available, remainingCapacity);
      if (selected.length === 0) {
        continue;
      }

      if (mode === "batch" && selected.length > remainingCapacity) {
        this.throwRemainingCapacityExceeded(
          request.tokenIdentifier,
          selected.length,
          remainingCapacity,
        );
        continue;
      }

      const availableIds = new Set(available.map((o) => o.output!.id!));
      for (const output of selected) {
        const id = output.output!.id!;
        if (!availableIds.has(id)) {
          this.throwInvalidSelectedOutput(id, request.tokenIdentifier, mode);
        }
        if (mode === "batch" && selectedIds.has(id)) {
          this.throwDuplicateSelectedOutput(id, request.tokenIdentifier);
        }
        selectedIds.add(id);
      }

      selectedByToken.set(request.tokenIdentifier, selected);
      if (mode === "batch") {
        remainingCapacity -= selected.length;
      }
    }

    if (selectedIds.size === 0) {
      return new Map();
    }

    const now = Date.now();
    for (const id of selectedIds) {
      this.localPendingMap.set(id, { lockedAt: now, operationId });
    }

    return selectedByToken;
  }

  private throwRemainingCapacityExceeded(
    tokenIdentifier: Bech32mTokenIdentifier,
    selectedLength: number,
    remainingCapacity: number,
  ): never {
    throw new SparkValidationError(
      `Selector for token ${tokenIdentifier} exceeded remaining capacity`,
      {
        field: "selectedOutputs",
        value: selectedLength,
        expected: `Less than or equal to remaining capacity (${remainingCapacity})`,
        tokenIdentifier,
      },
    );
  }

  private throwInvalidSelectedOutput(
    id: string,
    tokenIdentifier: Bech32mTokenIdentifier,
    mode: AcquireOutputsMode,
  ): never {
    if (mode === "single") {
      throw new Error(`Selected output ${id} is not in the available set`);
    }

    throw new SparkValidationError(
      `Selected output ${id} is not in the available set for token ${tokenIdentifier}`,
      {
        field: "selectedOutputs",
        value: id,
        expected: "Output ID from the token's available unlocked set",
        tokenIdentifier,
      },
    );
  }

  private throwDuplicateSelectedOutput(
    id: string,
    tokenIdentifier: Bech32mTokenIdentifier,
  ): never {
    throw new SparkValidationError(
      `Selected output ${id} was selected more than once`,
      {
        field: "selectedOutputs",
        value: id,
        expected: "Each selected output ID must be unique in the batch",
        tokenIdentifier,
      },
    );
  }

  private cleanupExpiredLocks(): void {
    const now = Date.now();
    for (const [id, lock] of this.localPendingMap) {
      if (now - lock.lockedAt > this.lockExpiryMs) {
        this.localPendingMap.delete(id);
      }
    }
  }

  private getAllKeys(): Bech32mTokenIdentifier[] {
    return Array.from(
      new Set([
        ...this.availableOutputs.keys(),
        ...this.serverPendingMap.keys(),
      ]),
    );
  }
}
