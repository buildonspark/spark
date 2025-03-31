import { secp256k1 } from "@noble/curves/secp256k1";
import { bytesToHex, hexToBytes } from "@noble/hashes/utils";
import { bech32m } from "@scure/base";
import { SparkAddress } from "../proto/spark.js";
import { NetworkType } from "../utils/network.js";

const AddressNetwork: Record<NetworkType, string> = {
  MAINNET: "sp",
  TESTNET: "spt",
  REGTEST: "sprt",
  SIGNET: "sps",
  LOCAL: "spl",
} as const;

export type SparkAddressFormat =
  `${(typeof AddressNetwork)[keyof typeof AddressNetwork]}1${string}`;

export interface SparkAddressData {
  identityPublicKey: string;
  network: NetworkType;
}

export function encodeSparkAddress(
  payload: SparkAddressData,
): SparkAddressFormat {
  isValidPublicKey(payload.identityPublicKey);

  const sparkAddressProto = SparkAddress.create({
    identityPublicKey: hexToBytes(payload.identityPublicKey),
  });

  const serializedPayload = SparkAddress.encode(sparkAddressProto).finish();
  const words = bech32m.toWords(serializedPayload);

  return bech32m.encode(
    AddressNetwork[payload.network],
    words,
    200,
  ) as SparkAddressFormat;
}

export function decodeSparkAddress(
  address: string,
  network: NetworkType,
): string {
  if (!address.startsWith(AddressNetwork[network])) {
    throw new Error("Invalid Spark address");
  }

  const decoded = bech32m.decode(address as SparkAddressFormat, 200);
  const payload = SparkAddress.decode(bech32m.fromWords(decoded.words));

  const publicKey = bytesToHex(payload.identityPublicKey);

  isValidPublicKey(publicKey);

  return publicKey;
}

export function isValidSparkAddress(address: string) {}

function isValidPublicKey(publicKey: string) {
  const point = secp256k1.ProjectivePoint.fromHex(publicKey);
  point.assertValidity();
}
