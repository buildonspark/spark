import { type SparkServiceDefinition } from "../proto/spark.js";
import { type SparkAuthnServiceDefinition } from "../proto/spark_authn.js";
import { type SparkTokenServiceDefinition } from "../proto/spark_token.js";
import { SparkError, type SparkErrorContextArg } from "./base.js";

/**
 * SparkRequestError should be used failed requests due to server errors or network issues.
 * This includes:
 * - API calls that failed or returned error(s)
 * - Network timeouts
 * - Connection refused
 * - DNS resolution failures
 * - SSL/TLS errors
 */
export class SparkRequestError extends SparkError {
  constructor(
    message: string,
    context: SparkErrorContextArg & {
      operation?:
        | keyof SparkServiceDefinition["methods"]
        | keyof SparkAuthnServiceDefinition["methods"]
        | keyof SparkTokenServiceDefinition["methods"];
      method?: "GET" | "POST";
    } = {},
  ) {
    super(message, context);
  }
}

/**
 * SparkValidationError should be used for any errors related to data validation in regards to the user's input,
 * This includes:
 * - Invalid signatures
 * - Malformed addresses
 * - Invalid proof of possession
 * - Invalid cryptographic parameters
 * - Data format validation failures
 */
export class SparkValidationError extends SparkError {
  constructor(message: string, context: SparkErrorContextArg = {}) {
    super(message, context);
  }
}

/**
 * SparkAuthenticationError should be used specifically for authentication and authorization failures,
 * such as invalid credentials or insufficient permissions.
 * This includes:
 * - Invalid API keys
 * - Expired tokens
 * - Insufficient permissions
 * - Authentication token validation failures
 * - Authorization failures
 */
export class SparkAuthenticationError extends SparkError {
  constructor(message: string, context: SparkErrorContextArg = {}) {
    super(message, context);
  }
}

/** @deprecated Use SparkValidationError instead. */
export const ValidationError = SparkValidationError;

/** @deprecated Use SparkRequestError instead. */
export const NetworkError = SparkRequestError;

/** @deprecated Use SparkAuthenticationError instead. */
export const AuthenticationError = SparkAuthenticationError;

/** @deprecated Use SparkError instead. */
export const ConfigurationError = SparkError;

/** @deprecated Use SparkError instead. */
export const InternalValidationError = SparkError;

/** @deprecated Use SparkRequestError instead. */
export const RPCError = SparkRequestError;

/** @deprecated Use SparkError instead. */
export const SparkSdkError = SparkError;
