import { secp256k1 } from "@noble/curves/secp256k1";
import { bytesToHex, hexToBytes } from "@noble/hashes/utils";
import { bech32m } from "@scure/base";
import { SparkAddress } from "../proto/spark.js";
import { NetworkType } from "../utils/network.js";

export type SparkAddressFormat = `sp1${string}`;

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
    network: payload.network,
  });

  const serializedPayload = SparkAddress.encode(sparkAddressProto).finish();
  const words = bech32m.toWords(serializedPayload);
  return bech32m.encode("sp", words, 200) as SparkAddressFormat;
}

export function decodeSparkAddress(
  address: string,
  network: NetworkType,
): SparkAddressData {
  if (!address.startsWith("sp1")) {
    throw new Error("Invalid Spark address");
  }

  const decoded = bech32m.decode(address as SparkAddressFormat, 200);
  const payload = SparkAddress.decode(bech32m.fromWords(decoded.words));

  if (network !== payload.network) {
    throw new Error("Network mismatch");
  }

  const publicKey = bytesToHex(payload.identityPublicKey);

  isValidPublicKey(publicKey);

  return {
    identityPublicKey: publicKey,
    network: payload.network as NetworkType,
  };
}

export function isValidSparkAddress(address: string) {}

function isValidPublicKey(publicKey: string) {
  const point = secp256k1.ProjectivePoint.fromHex(publicKey);
  point.assertValidity();
}
