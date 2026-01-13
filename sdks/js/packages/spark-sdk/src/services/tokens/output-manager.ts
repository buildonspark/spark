import { Mutex } from "async-mutex";
import { OutputWithPreviousTransactionData } from "../../proto/spark_token.js";
import { TokenOutputsMap } from "../../spark-wallet/types.js";
import { Bech32mTokenIdentifier } from "../../utils/token-identifier.js";

export type TokenOutputLockReason = "pending_received" | "pending_sent";

export type TokenOutputLock = {
  lockedAt: number;
  reason: TokenOutputLockReason;
  operationId?: string;
};

export type AcquiredOutputs = {
  outputs: OutputWithPreviousTransactionData[];
  release: () => Promise<void>;
};

export class TokenOutputManager {
  private outputs: TokenOutputsMap = new Map();
  private locks: Map<string, TokenOutputLock> = new Map();
  private readonly mutex = new Mutex();
  private readonly lockExpiryMs: number;

  constructor(lockExpiryMs: number = 30000) {
    this.lockExpiryMs = lockExpiryMs;
  }

  /**
   * Set the token outputs map (typically after sync).
   */
  async setOutputs(newOutputs: TokenOutputsMap): Promise<void> {
    await this.mutex.runExclusive(() => {
      this.outputs = newOutputs;
      this.cleanupStaleLocks();
    });
  }

  /**
   * Get all outputs for a token (including locked ones).
   */
  async getAllOutputs(
    tokenIdentifier: Bech32mTokenIdentifier,
  ): Promise<OutputWithPreviousTransactionData[]> {
    return await this.mutex.runExclusive(() => {
      return [...(this.outputs.get(tokenIdentifier) ?? [])];
    });
  }

  /**
   * Check if outputs map has a token identifier.
   */
  async hasTokenIdentifier(
    tokenIdentifier: Bech32mTokenIdentifier,
  ): Promise<boolean> {
    return await this.mutex.runExclusive(() => {
      return this.outputs.has(tokenIdentifier);
    });
  }

  /**
   * Get all token identifiers in the map.
   */
  async getTokenIdentifiers(): Promise<Bech32mTokenIdentifier[]> {
    return await this.mutex.runExclusive(() => {
      return [...this.outputs.keys()];
    });
  }

  /**
   * Iterate over entries (snapshot).
   */
  async entries(): Promise<
    [Bech32mTokenIdentifier, OutputWithPreviousTransactionData[]][]
  > {
    return await this.mutex.runExclusive(() => {
      return [...this.outputs.entries()];
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
        this.locks.set(id, {
          lockedAt: now,
          reason: "pending_sent",
          operationId,
        });
        lockedIds.push(id);
      }

      const release = async () => {
        await this.releaseOutputsByIds(lockedIds);
      };

      return { outputs: selected, release };
    });
  }

  /**
   * Lock specific outputs by their data.
   */
  async lockOutputs(
    outputs: OutputWithPreviousTransactionData[],
    reason: TokenOutputLockReason = "pending_sent",
    operationId?: string,
  ): Promise<void> {
    await this.mutex.runExclusive(() => {
      const now = Date.now();
      for (const output of outputs) {
        const id = output.output!.id!;
        this.locks.set(id, { lockedAt: now, reason, operationId });
      }
    });
  }

  /**
   * Lock specific outputs by ID
   */
  async lockOutputsByIds(
    outputIds: string[],
    reason: TokenOutputLockReason,
    operationId?: string,
  ): Promise<void> {
    await this.mutex.runExclusive(() => {
      const now = Date.now();
      for (const id of outputIds) {
        this.locks.set(id, { lockedAt: now, reason, operationId });
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
        this.locks.delete(id);
      }
    });
  }

  /**
   * Release outputs by ID.
   */
  async releaseOutputsByIds(outputIds: string[]): Promise<void> {
    await this.mutex.runExclusive(() => {
      for (const id of outputIds) {
        this.locks.delete(id);
      }
    });
  }

  /**
   * Check if an output is locked.
   */
  async isLocked(outputId: string): Promise<boolean> {
    return await this.mutex.runExclusive(() => {
      this.cleanupExpiredLocks();
      return this.locks.has(outputId);
    });
  }

  /**
   * Check if outputs map is empty.
   */
  async isEmpty(): Promise<boolean> {
    return await this.mutex.runExclusive(() => {
      return this.outputs.size === 0;
    });
  }

  /**
   * Get size of outputs map (number of token identifiers).
   */
  async size(): Promise<number> {
    return await this.mutex.runExclusive(() => {
      return this.outputs.size;
    });
  }

  /**
   * Clear all outputs and locks.
   */
  async clear(): Promise<void> {
    await this.mutex.runExclusive(() => {
      this.outputs.clear();
      this.locks.clear();
    });
  }

  private getUnlockedOutputsInternal(
    tokenIdentifier: Bech32mTokenIdentifier,
  ): OutputWithPreviousTransactionData[] {
    const outputs = this.outputs.get(tokenIdentifier) ?? [];
    return outputs.filter((o) => !this.locks.has(o.output!.id!));
  }

  private cleanupExpiredLocks(): void {
    const now = Date.now();
    for (const [id, lock] of this.locks) {
      if (now - lock.lockedAt > this.lockExpiryMs) {
        this.locks.delete(id);
      }
    }
  }

  private cleanupStaleLocks(): void {
    const allOutputIds = new Set<string>();
    for (const outputs of this.outputs.values()) {
      for (const o of outputs) {
        allOutputIds.add(o.output!.id!);
      }
    }
    for (const lockedId of this.locks.keys()) {
      if (!allOutputIds.has(lockedId)) {
        this.locks.delete(lockedId);
      }
    }
  }
}
