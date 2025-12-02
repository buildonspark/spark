import { SparkValidationError } from "@buildonspark/spark-sdk";

/**
 * Returns true when the input is already in NFC normalisation form.
 * JavaScript strings are UTF-16 encoded, so any JavaScript string is
 * already valid Unicode.  However, we still need to ensure canonical
 * equivalence so that, for example, \u00E9 (é) and \u0065\u0301 (é)
 * are treated identically.  We do this by comparing the original
 * string to its NFC-normalised representation.
 */
function isNfcNormalized(value: string): boolean {
  return value.normalize("NFC") === value;
}

const MIN_NAME_SIZE = 3; // bytes
const MAX_NAME_SIZE = 20; // bytes
const MIN_SYMBOL_SIZE = 3; // bytes
const MAX_SYMBOL_SIZE = 6; // bytes
const MAX_DECIMALS = 255; // fits into single byte
const MAXIMUM_MAX_SUPPLY = (1n << 128n) - 1n; // fits into 16 bytes (u128)

export function validateTokenParameters(
  tokenName: string,
  tokenTicker: string,
  decimals: number,
  maxSupply: bigint,
) {
  if (!isNfcNormalized(tokenName)) {
    throw new SparkValidationError("Token name must be NFC-normalised UTF-8", {
      field: "tokenName",
      value: tokenName,
      expected: "NFC normalised string",
    });
  }

  if (!isNfcNormalized(tokenTicker)) {
    throw new SparkValidationError(
      "Token ticker must be NFC-normalised UTF-8",
      {
        field: "tokenTicker",
        value: tokenTicker,
        expected: "NFC normalised string",
      },
    );
  }

  const nameBytes = Buffer.from(tokenName, "utf-8").length;
  if (nameBytes < MIN_NAME_SIZE || nameBytes > MAX_NAME_SIZE) {
    throw new SparkValidationError(
      `Token name must be between ${MIN_NAME_SIZE} and ${MAX_NAME_SIZE} bytes`,
      {
        field: "tokenName",
        value: tokenName,
        actualLength: nameBytes,
        expected: `>=${MIN_NAME_SIZE} and <=${MAX_NAME_SIZE}`,
      },
    );
  }

  const tickerBytes = Buffer.from(tokenTicker, "utf-8").length;
  if (tickerBytes < MIN_SYMBOL_SIZE || tickerBytes > MAX_SYMBOL_SIZE) {
    throw new SparkValidationError(
      `Token ticker must be between ${MIN_SYMBOL_SIZE} and ${MAX_SYMBOL_SIZE} bytes`,
      {
        field: "tokenTicker",
        value: tokenTicker,
        actualLength: tickerBytes,
        expected: `>=${MIN_SYMBOL_SIZE} and <=${MAX_SYMBOL_SIZE}`,
      },
    );
  }

  if (
    !Number.isSafeInteger(decimals) ||
    decimals < 0 ||
    decimals > MAX_DECIMALS
  ) {
    throw new SparkValidationError(
      `Decimals must be an integer between 0 and ${MAX_DECIMALS}`,
      {
        field: "decimals",
        value: decimals,
        expected: `>=0 and <=${MAX_DECIMALS}`,
      },
    );
  }

  if (maxSupply < 0n || maxSupply > MAXIMUM_MAX_SUPPLY) {
    throw new SparkValidationError(`maxSupply must be between 0 and 2^128-1`, {
      field: "maxSupply",
      value: maxSupply.toString(),
      expected: `>=0 and <=${MAXIMUM_MAX_SUPPLY.toString()}`,
    });
  }
}
