import { SparkError } from "./base.js";
import { type SparkServiceDefinition } from "../proto/spark.js";
import { type SparkAuthnServiceDefinition } from "../proto/spark_authn.js";
import { type SparkTokenServiceDefinition } from "../proto/spark_token.js";

/**
 * SparkRequestError should be used for any errors related to requests or network
 * communication, such as failed HTTP requests, timeouts, or connection issues.
 * This includes:
 * - Failed API calls
 * - Network timeouts
 * - Connection refused
 * - DNS resolution failures
 * - SSL/TLS errors
 */
export class SparkRequestError extends SparkError {
  constructor(
    message: string,
    context: Record<string, unknown> & {
      operation?:
        | keyof SparkServiceDefinition["methods"]
        | keyof SparkAuthnServiceDefinition["methods"]
        | keyof SparkTokenServiceDefinition["methods"];
      method?: "GET" | "POST";
    } = {},
    originalError?: Error,
  ) {
    super(message, context, originalError);
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
  constructor(
    message: string,
    context: Record<string, unknown> = {},
    originalError?: Error,
  ) {
    super(message, context, originalError);
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
  constructor(
    message: string,
    context: Record<string, unknown> = {},
    originalError?: Error,
  ) {
    super(message, context, originalError);
  }
}
