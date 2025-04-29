export class SparkSDKError extends Error {
  public readonly context: Record<string, unknown>;
  public readonly originalError?: Error;

  constructor(
    message: string,
    context: Record<string, unknown> = {},
    originalError?: Error,
  ) {
    super(message);
    this.name = this.constructor.name;
    this.context = context;
    this.originalError = originalError;

    if (Error.captureStackTrace) {
      Error.captureStackTrace(this, this.constructor);
    }
  }

  public toString(): string {
    const contextStr = Object.entries(this.context)
      .map(([key, value]) => `${key}: ${JSON.stringify(value)}`)
      .join(", ");

    const originalErrorStr = this.originalError
      ? `\nOriginal Error: ${this.originalError.message}`
      : "";

    return `${this.name}: ${this.message}${contextStr ? `\nContext: ${contextStr}` : ""}${originalErrorStr}`;
  }

  public toJSON(): Record<string, unknown> {
    return {
      name: this.name,
      message: this.message,
      context: this.context,
      originalError: this.originalError
        ? {
            name: this.originalError.name,
            message: this.originalError.message,
            stack: this.originalError.stack,
          }
        : undefined,
      stack: this.stack,
    };
  }
}
