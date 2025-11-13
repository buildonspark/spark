/**
 * Cross-platform monotonic clock
 * Returns time in milliseconds with sub-millisecond precision.
 */
function getMonotonicTime(): number {
  // Node.js
  if (typeof process !== "undefined" && process.hrtime) {
    const [seconds, nanoseconds] = process.hrtime();
    return seconds * 1000 + nanoseconds / 1_000_000;
  }

  // Browser or React Native with performance.now()
  if (typeof performance !== "undefined" && performance.now) {
    return performance.now();
  }

  throw new Error("Monotonic time not available");
}

export class ServerTimeSync {
  private estimatedServerOffsetMs: number | null = null;

  /**
   * Records a time synchronization sample from a gRPC response.
   *
   * @param serverEndDateHeader - Request end time header 'date' (RFC 2822 format)
   * @param serverProcessingTimeMs - Time spent processing on the server
   * @param clientStartMonotonicMs - Monotonic time when the request was sent
   * @param clientEndMonotonicMs - Monotonic time when the response was received
   */
  public recordSync(
    serverEndDateHeader: string,
    serverProcessingTimeMs: number,
    clientStartMonotonicMs: number,
    clientEndMonotonicMs: number,
  ): void {
    const serverEndTimeMs = new Date(serverEndDateHeader).getTime();

    if (isNaN(serverEndTimeMs)) {
      console.warn("Invalid server date header:", serverEndDateHeader);
      return;
    }

    const roundTripTimeMs =
      clientEndMonotonicMs - clientStartMonotonicMs - serverProcessingTimeMs;
    const estimatedServerEndTs = serverEndTimeMs + roundTripTimeMs / 2;
    this.estimatedServerOffsetMs = estimatedServerEndTs - clientEndMonotonicMs;
  }

  /**
   * Gets the current tamper-proof server time.
   * This time is calculated based on the server offset and monotonic clock,
   * making it resistant to local system time changes.
   *
   * @returns Current server time as a Date object, or null if not synced yet
   */
  public getCurrentServerTime(): Date | null {
    if (this.estimatedServerOffsetMs === null) {
      return null;
    }

    const currentMonotonicMs = getMonotonicTime();
    const currentServerTimeMs =
      currentMonotonicMs + this.estimatedServerOffsetMs;

    return new Date(currentServerTimeMs);
  }

  /**
   * Gets the offset between server time and local monotonic time.
   * This can be used to understand the time difference.
   *
   * @returns Offset in milliseconds, or null if not synced yet
   */
  public getOffset(): number | null {
    return this.estimatedServerOffsetMs;
  }

  /**
   * Checks if the time sync has been initialized with at least one sample.
   */
  public isSynced(): boolean {
    return this.estimatedServerOffsetMs !== null;
  }

  /**
   * Resets all time synchronization data.
   */
  public reset(): void {
    this.estimatedServerOffsetMs = null;
  }
}

export { getMonotonicTime };
