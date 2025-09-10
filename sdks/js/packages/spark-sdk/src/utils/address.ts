import { BinaryWriter } from "@bufbuild/protobuf/wire";
import { schnorr, secp256k1 } from "@noble/curves/secp256k1";
import { bytesToNumberBE } from "@noble/curves/utils";
import { bytesToHex, hexToBytes } from "@noble/hashes/utils";
import { bech32m } from "@scure/base";
import { UUID } from "uuidv7";
import { ValidationError } from "../errors/index.js";
import { Timestamp } from "../proto/google/protobuf/timestamp.js";
import {
  SatsPayment,
  SparkAddress,
  SparkInvoiceFields,
  TokensPayment,
} from "../proto/spark.js";
import { HashSparkInvoice } from "./invoice-hashing.js";
import { NetworkType } from "./network.js";

const BECH32M_LIMIT = 1024;

const AddressNetwork: Record<NetworkType, string> = {
  MAINNET: "spark",
  TESTNET: "sparkt",
  REGTEST: "sparkrt",
  SIGNET: "sparks",
  LOCAL: "sparkl",
} as const;
const LegacyAddressNetwork: Record<NetworkType, string> = {
  MAINNET: "sp",
  TESTNET: "spt",
  REGTEST: "sprt",
  SIGNET: "sps",
  LOCAL: "spl",
} as const;

type Bech32String = `${string}1${string}`;
export type SparkAddressFormat =
  `${(typeof AddressNetwork)[keyof typeof AddressNetwork]}1${string}`;
export type LegacySparkAddressFormat =
  `${(typeof LegacyAddressNetwork)[keyof typeof LegacyAddressNetwork]}1${string}`;

export interface SparkAddressData {
  identityPublicKey: string;
  network: NetworkType;
  sparkInvoiceFields?: SparkInvoiceFields;
}

export interface DecodedSparkAddressData {
  identityPublicKey: string;
  network: NetworkType;
  sparkInvoiceFields?: {
    version: number;
    id: string;
    paymentType?:
      | { type: "tokens"; tokenIdentifier?: string; amount?: bigint }
      | { type: "sats"; amount?: number };
    memo?: string;
    senderPublicKey?: string;
    expiryTime?: Date;
  };
  signature?: string;
}

export function encodeSparkAddress(
  payload: SparkAddressData,
): SparkAddressFormat {
  return encodeSparkAddressWithSignature(payload);
}

export function encodeSparkAddressWithSignature(
  payload: SparkAddressData,
  signature?: Uint8Array,
): SparkAddressFormat {
  try {
    isValidPublicKey(payload.identityPublicKey);
    const identityPublicKey = hexToBytes(payload.identityPublicKey);

    let sparkInvoiceFields: SparkInvoiceFields | undefined;
    if (payload.sparkInvoiceFields) {
      validateSparkInvoiceFields(payload.sparkInvoiceFields);
      sparkInvoiceFields = payload.sparkInvoiceFields;
    }

    const w = new BinaryWriter();

    // SparkAddress.identity_public_key (1)
    w.uint32(10).bytes(identityPublicKey);
    // SparkAddress.spark_invoice_fields (2) with canonical inner order
    if (sparkInvoiceFields) {
      const inner = encodeSparkInvoiceFieldsV1Canonical(sparkInvoiceFields);
      w.uint32(18).bytes(inner);
    }
    // SparkAddress.signature (3)
    if (signature && signature.length) {
      w.uint32(26).bytes(signature);
    }

    const serializedPayload = w.finish();
    const words = bech32m.toWords(serializedPayload);

    return bech32mEncode(
      AddressNetwork[payload.network],
      words,
    ) as SparkAddressFormat;
  } catch (error) {
    throw new ValidationError(
      "Failed to encode Spark address",
      {
        field: "publicKey",
        value: payload.identityPublicKey,
      },
      error as Error,
    );
  }
}

export function decodeSparkAddress(
  address: string,
  network: NetworkType,
): DecodedSparkAddressData {
  try {
    const decoded = bech32mDecode(address as SparkAddressFormat);

    if (network !== getNetworkFromSparkAddress(address)) {
      throw new ValidationError("Invalid Spark address prefix", {
        field: "address",
        value: address,
        expected: `prefix='${AddressNetwork[network]}' or '${LegacyAddressNetwork[network]}'`,
      });
    }

    const payload = SparkAddress.decode(bech32m.fromWords(decoded.words));

    const { identityPublicKey, sparkInvoiceFields, signature } = payload;

    const identityPubkeyHex = bytesToHex(identityPublicKey);
    const signatureHex = signature ? bytesToHex(signature) : undefined;
    isValidPublicKey(identityPubkeyHex);

    return {
      identityPublicKey: identityPubkeyHex,
      network,
      sparkInvoiceFields: sparkInvoiceFields && {
        version: sparkInvoiceFields.version,
        id: UUID.ofInner(sparkInvoiceFields.id).toString(),
        paymentType: sparkInvoiceFields.paymentType
          ? sparkInvoiceFields.paymentType.$case === "tokensPayment"
            ? {
                type: "tokens" as const,
                tokenIdentifier: sparkInvoiceFields.paymentType.tokensPayment
                  .tokenIdentifier
                  ? bytesToHex(
                      sparkInvoiceFields.paymentType.tokensPayment
                        .tokenIdentifier,
                    )
                  : undefined,
                amount: sparkInvoiceFields.paymentType.tokensPayment.amount
                  ? bytesToNumberBE(
                      sparkInvoiceFields.paymentType.tokensPayment.amount,
                    )
                  : undefined,
              }
            : sparkInvoiceFields.paymentType.$case === "satsPayment"
              ? {
                  type: "sats" as const,
                  amount: sparkInvoiceFields.paymentType.satsPayment.amount,
                }
              : undefined
          : undefined,
        memo: sparkInvoiceFields.memo,
        senderPublicKey: sparkInvoiceFields.senderPublicKey
          ? bytesToHex(sparkInvoiceFields.senderPublicKey)
          : undefined,
        expiryTime: sparkInvoiceFields.expiryTime,
      },
      signature: signatureHex,
    };
  } catch (error) {
    if (error instanceof ValidationError) {
      throw error;
    }
    throw new ValidationError(
      "Failed to decode Spark address",
      {
        field: "address",
        value: address,
      },
      error as Error,
    );
  }
}

const PrefixToNetwork: Record<string, NetworkType> = Object.fromEntries(
  Object.entries(AddressNetwork).map(([k, v]) => [v, k as NetworkType]),
) as Record<string, NetworkType>;
const LegacyPrefixToNetwork: Record<string, NetworkType> = Object.fromEntries(
  Object.entries(LegacyAddressNetwork).map(([k, v]) => [v, k as NetworkType]),
) as Record<string, NetworkType>;

export function getNetworkFromSparkAddress(address: string): NetworkType {
  const { prefix } = bech32mDecode(address);
  const network = PrefixToNetwork[prefix] ?? LegacyPrefixToNetwork[prefix];
  if (!network) {
    throw new ValidationError("Invalid Spark address prefix", {
      field: "network",
      value: address,
      expected:
        "prefix='spark1', 'sparkt1', 'sparkrt1', 'sparks1', 'sparkl1' or legacy ('sp1', 'spt1', 'sprt1', 'sps1', 'spl1')",
    });
  }
  return network;
}
// Checks if a string is a legacy spark address.
export function isLegacySparkAddress(
  address: string,
): address is LegacySparkAddressFormat {
  try {
    const { prefix } = bech32mDecode(address);
    return prefix in LegacyPrefixToNetwork;
  } catch (error) {
    return false;
  }
}
export function isValidSparkAddress(address: string) {
  try {
    const network = getNetworkFromSparkAddress(address);
    decodeSparkAddress(address, network);
    return true;
  } catch (error) {
    if (error instanceof ValidationError) {
      throw error;
    }
    throw new ValidationError(
      "Invalid Spark address",
      {
        field: "address",
        value: address,
      },
      error as Error,
    );
  }
}

export function isValidPublicKey(publicKey: string) {
  try {
    const point = secp256k1.Point.fromHex(publicKey);
    point.assertValidity();
  } catch (error) {
    throw new ValidationError(
      "Invalid public key",
      {
        field: "publicKey",
        value: publicKey,
      },
      error as Error,
    );
  }
}

export function validateSparkInvoiceFields(
  sparkInvoiceFields: SparkInvoiceFields,
) {
  const { version, id, paymentType, memo, senderPublicKey } =
    sparkInvoiceFields;
  if (version !== 1) {
    throw new ValidationError("Version must be 1", {
      field: "version",
      value: version,
    });
  }
  // ID is required and must be a valid UUID
  try {
    UUID.ofInner(id);
  } catch (error) {
    throw new ValidationError(
      "Invalid id",
      {
        field: "id",
        value: id,
      },
      error as Error,
    );
  }
  if (senderPublicKey) {
    try {
      isValidPublicKey(bytesToHex(senderPublicKey));
    } catch (error) {
      throw new ValidationError(
        "Invalid sender public key",
        {
          field: "senderPublicKey",
          value: senderPublicKey,
        },
        error as Error,
      );
    }
  }
  if (memo) {
    const encoder = new TextEncoder();
    const memoBytes = encoder.encode(memo);
    if (memoBytes.length > 120) {
      throw new ValidationError(
        "Memo exceeds the maximum allowed byte length of 120.",
        {
          field: "memo",
          value: memo,
          expected: "less than 120 bytes",
        },
      );
    }
  }
  if (paymentType) {
    if (paymentType.$case === "tokensPayment") {
      const MAX_UINT128 = BigInt(2 ** 128 - 1);
      const { amount: tokensAmount, tokenIdentifier } =
        paymentType.tokensPayment;
      if (tokenIdentifier) {
        if (!(tokenIdentifier instanceof Uint8Array)) {
          throw new ValidationError("Token identifier must be Uint8Array", {
            field: "paymentType.tokensPayment.tokenIdentifier",
            value: tokenIdentifier,
          });
        }
        if (tokenIdentifier.length !== 32) {
          throw new ValidationError("Token identifier must be 32 bytes", {
            field: "paymentType.tokensPayment.tokenIdentifier",
            value: tokenIdentifier,
          });
        }
      }
      if (tokensAmount) {
        if (tokensAmount.length > 16) {
          throw new ValidationError("Amount must be less than 16 bytes", {
            field: "paymentType.tokensPayment.amount",
            value: tokensAmount,
          });
        }
        const tokensAmountBigInt = bytesToNumberBE(tokensAmount);
        if (tokensAmountBigInt < 0 || tokensAmountBigInt > MAX_UINT128) {
          throw new ValidationError(
            "Asset amount must be between 0 and MAX_UINT128",
            {
              field: "amount",
              value: tokensAmount,
            },
          );
        }
      }
    } else if (paymentType.$case === "satsPayment") {
      const { amount } = paymentType.satsPayment;
      if (amount) {
        const MAX_SATS_AMOUNT = 2_100_000_000_000_000; // 21_000_000 BTC * 100_000_000 sats/BTC
        if (amount < 0) {
          throw new ValidationError(
            "Amount must be greater than or equal to 0",
            {
              field: "paymentType.satsPayment.amount",
              value: amount,
            },
          );
        }
        if (amount > MAX_SATS_AMOUNT) {
          throw new ValidationError(
            `Amount must be less than ${MAX_SATS_AMOUNT} sats`,
          );
        }
      }
    } else {
      throw new ValidationError("Invalid payment type", {
        field: "paymentType",
        value: paymentType,
      });
    }
  }
}

export function validateSparkInvoiceSignature(invoice: SparkAddressFormat) {
  try {
    const decoded = bech32mDecode(invoice as SparkAddressFormat);
    const network = getNetworkFromSparkAddress(invoice);
    const payload = SparkAddress.decode(bech32m.fromWords(decoded.words));
    const { identityPublicKey, sparkInvoiceFields, signature } = payload;
    if (!sparkInvoiceFields) {
      throw new ValidationError("Spark invoice fields are required", {
        field: "sparkInvoiceFields",
        value: sparkInvoiceFields,
      });
    }
    if (!signature) {
      throw new ValidationError("Signature is required", {
        field: "signature",
        value: signature,
      });
    }
    if (!identityPublicKey) {
      throw new ValidationError("Identity public key is required", {
        field: "identityPublicKey",
        value: identityPublicKey,
      });
    }
    const hash = HashSparkInvoice(
      sparkInvoiceFields,
      identityPublicKey,
      network,
    );
    const sec256k1PublicKey = secp256k1.Point.fromHex(identityPublicKey);
    const compressed = sec256k1PublicKey.toBytes(true);
    const xOnly = compressed.slice(1);
    const isValid = schnorr.verify(signature, hash, xOnly);
    if (!isValid) {
      throw new ValidationError("Invalid signature", {
        field: "signature",
        value: signature,
      });
    }
  } catch (error) {
    if (error instanceof ValidationError) {
      throw error;
    }
    throw new ValidationError(
      "Failed to validate Spark invoice signature",
      {
        field: "invoice",
        value: invoice,
      },
      error as Error,
    );
  }
}

export function toProtoTimestamp(date: Date) {
  const ms = date.getTime();
  return { seconds: Math.floor(ms / 1000), nanos: (ms % 1000) * 1_000_000 };
}

export function assertBech32(s: string): asserts s is Bech32String {
  const i = s.lastIndexOf("1");
  if (i <= 0 || i >= s.length - 1) throw new Error("invalid bech32 string");
}

export function bech32mDecode(address: string) {
  assertBech32(address);
  return bech32m.decode(address, BECH32M_LIMIT);
}
function bech32mEncode(prefix: string, words: number[] | Uint8Array) {
  return bech32m.encode(prefix, words, BECH32M_LIMIT) as SparkAddressFormat;
}

function encodeSparkInvoiceFieldsV1Canonical(
  f: SparkInvoiceFields,
): Uint8Array {
  const w = new BinaryWriter();
  // version (1)
  if (f.version !== 0) w.uint32(8).uint32(f.version);
  // id (2)
  if (f.id && f.id.length) w.uint32(18).bytes(f.id);
  // memo (5)
  if (f.memo !== undefined) w.uint32(42).string(f.memo);
  // sender_public_key (6)
  if (f.senderPublicKey !== undefined) w.uint32(50).bytes(f.senderPublicKey);
  // expiry_time (7)
  if (f.expiryTime !== undefined) {
    Timestamp.encode(
      toProtoTimestamp(f.expiryTime),
      w.uint32(58).fork(),
    ).join();
  }
  // payment_type oneof last: tokens (3) or sats (4)
  switch (f.paymentType?.$case) {
    case "tokensPayment":
      TokensPayment.encode(
        f.paymentType.tokensPayment,
        w.uint32(26).fork(),
      ).join();
      break;
    case "satsPayment":
      SatsPayment.encode(f.paymentType.satsPayment, w.uint32(34).fork()).join();
      break;
  }
  return w.finish();
}

export function isSafeForNumber(bi: bigint): boolean {
  return (
    bi >= BigInt(Number.MIN_SAFE_INTEGER) &&
    bi <= BigInt(Number.MAX_SAFE_INTEGER)
  );
}
