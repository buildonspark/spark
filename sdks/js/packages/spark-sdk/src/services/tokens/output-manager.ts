import { Mutex } from "async-mutex";
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

export type AcquiredOutputs = {
  outputs: OutputWithPreviousTransactionData[];
  release: () => Promise<void>;
};

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
      return [...(this.serverPendingMap.get(tokenIdentifier) ?? [])];
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
   * Returns the selected outputs and a release function.
   *
   * @param tokenIdentifier - The token to select from
   * @param selector - Function to select outputs from available (unlocked) outputs
   * @param operationId - name of the operation for debugging purposes
   * @returns AcquiredOutputs with outputs and release function
   */
  async acquireOutputs(
    tokenIdentifier: Bech32mTokenIdentifier,
    selector: (
      outputs: OutputWithPreviousTransactionData[],
    ) => OutputWithPreviousTransactionData[],
    operationId?: string,
  ): Promise<AcquiredOutputs> {
    return await this.mutex.runExclusive(() => {
      this.cleanupExpiredLocks();

      const available = this.getUnlockedOutputsInternal(tokenIdentifier);
      const selected = selector(available);

      if (selected.length === 0) {
        return {
          outputs: [],
          release: async () => {},
        };
      }

      // Validate that all selected outputs are from the available set
      const availableIds = new Set(available.map((o) => o.output!.id!));
      for (const output of selected) {
        const id = output.output!.id!;
        if (!availableIds.has(id)) {
          throw new Error(`Selected output ${id} is not in the available set`);
        }
      }

      const now = Date.now();
      const lockedIds: string[] = [];
      for (const output of selected) {
        const id = output.output!.id!;
        this.localPendingMap.set(id, { lockedAt: now, operationId });
        lockedIds.push(id);
      }

      const release = async () => {
        await this.releaseOutputsByIds(lockedIds);
      };

      return { outputs: selected, release };
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
   * Release outputs.
   */
  async releaseOutputs(
    outputs: OutputWithPreviousTransactionData[],
  ): Promise<void> {
    await this.mutex.runExclusive(() => {
      for (const output of outputs) {
        const id = output.output!.id!;
        this.localPendingMap.delete(id);
      }
    });
  }

  /**
   * Release outputs by ID.
   */
  async releaseOutputsByIds(outputIds: string[]): Promise<void> {
    await this.mutex.runExclusive(() => {
      for (const id of outputIds) {
        this.localPendingMap.delete(id);
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
