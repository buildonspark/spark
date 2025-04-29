import { SparkSDKError } from "./base.js";

/**
 * NetworkError should be used for any errors related to network communication,
 * such as failed HTTP requests, timeouts, or connection issues.
 * This includes:
 * - Failed API calls
 * - Network timeouts
 * - Connection refused
 * - DNS resolution failures
 * - SSL/TLS errors
 */
export class NetworkError extends SparkSDKError {
  constructor(
    message: string,
    context: {
      url?: string;
      method?: string;
      statusCode?: number;
      response?: unknown;
      errorCount?: number;
      errors?: string;
      operation?: string;
      nodeSignaturesCount?: number;
    } = {},
    originalError?: Error,
  ) {
    super(message, context, originalError);
  }
}

/**
 * ValidationError should be used for any errors related to data validation in regards to the user's input,
 * This includes:
 * - Invalid signatures
 * - Malformed addresses
 * - Invalid proof of possession
 * - Invalid cryptographic parameters
 * - Data format validation failures
 */
export class ValidationError extends SparkSDKError {
  constructor(
    message: string,
    context: {
      field?: string;
      value?: unknown;
      expected?: unknown;
      expectedLength?: number;
      actualLength?: number;
      rValue?: bigint;
      fieldPrime?: bigint;
      sValue?: bigint;
      groupOrder?: bigint;
      index?: number;
      treeLength?: number;
      addressNodesLength?: number;
    } = {},
    originalError?: Error,
  ) {
    super(message, context, originalError);
  }
}

/**
 * InternalValidationError should be used for any errors related to internal data validation
 * that is not related to the user's input.
 * This includes:
 * - Invalid SO responses
 */
export class InternalValidationError extends SparkSDKError {
  constructor(
    message: string,
    context: {
      field?: string;
      value?: unknown;
      expected?: unknown;
      outputIndex?: number;
      keyshareInfo?: unknown;
      signingOperators?: unknown;
    } = {},
    originalError?: Error,
  ) {
    super(message, context, originalError);
  }
}

/**
 * AuthenticationError should be used specifically for authentication and authorization failures,
 * such as invalid credentials or insufficient permissions.
 * This includes:
 * - Invalid API keys
 * - Expired tokens
 * - Insufficient permissions
 * - Authentication token validation failures
 * - Authorization failures
 */
export class AuthenticationError extends SparkSDKError {
  constructor(
    message: string,
    context: {
      endpoint?: string;
      reason?: string;
    } = {},
    originalError?: Error,
  ) {
    super(message, context, originalError);
  }
}

/**
 * RPCError should be used for errors that occur during RPC (Remote Procedure Call) operations,
 * such as invalid RPC parameters or RPC-specific failures.
 * This includes:
 * - Invalid RPC method calls
 * - RPC parameter validation failures
 * - RPC-specific error codes
 * - RPC protocol errors
 */
export class RPCError extends SparkSDKError {
  constructor(
    message: string,
    context: {
      method?: string;
      params?: unknown;
      code?: number;
    } = {},
    originalError?: Error,
  ) {
    super(message, context, originalError);
  }
}

/**
 * ConfigurationError should be used for errors related to SDK configuration,
 * such as missing or invalid configuration values.
 * This includes:
 * - Missing required configuration
 * - Invalid configuration values
 * - Configuration format errors
 * - Environment-specific configuration issues
 */
export class ConfigurationError extends SparkSDKError {
  constructor(
    message: string,
    context: {
      configKey?: string;
      value?: unknown;
    } = {},
    originalError?: Error,
  ) {
    super(message, context, originalError);
  }
}

/***
 * NotImplementedError should be used for any errors related to features that are not yet implemented.
 */
export class NotImplementedError extends SparkSDKError {
  constructor(
    message: string,
    context?: Record<string, unknown>,
    originalError?: Error,
  ) {
    super(message, context, originalError);
  }
}
