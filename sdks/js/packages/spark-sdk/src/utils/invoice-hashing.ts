import type { NetworkType } from "./network.js";
import type { SparkInvoiceFields } from "../proto/spark.js";
import { sha256 } from "@noble/hashes/sha2";

export function HashSparkInvoice(
  sparkInvoiceFields: SparkInvoiceFields,
  receiverPublicKey: Uint8Array,
  network: NetworkType,
) {
  if (!sparkInvoiceFields) {
    throw new Error("Missing sparkInvoiceFields");
  }
  if (!receiverPublicKey) {
    throw new Error("Receiver public key is required");
  }
  if (!network) {
    throw new Error("Network is required");
  }

  switch (sparkInvoiceFields.version) {
    case 1: {
      const hash = HashSparkInvoiceV1(
        sparkInvoiceFields,
        receiverPublicKey,
        network,
      );
      return hash;
    }
    default:
      throw new Error(
        `Unsupported invoice version: ${sparkInvoiceFields.version}`,
      );
  }
}

// HashSparkInvoiceV1 computes a deterministic hash of SparkInvoiceFields by:
// - Hashing each field (or group) separately using SHA-256, in a fixed order
// - Concatenating those field-level hashes
// - Hashing the concatenation once more with SHA-256
//
// Field order and encoding:
// 1) version: uint32 big-endian (required)
// 2) id: 16 bytes (required)
// 3) network: 4 bytes (required)
// 4) receiver_public_key: 33 bytes (required)
// 5) payment_type discriminator (1 byte) + contents:
//   - TokensPayment: discriminator {1}
//     token_identifier: 32 bytes (0-filled if nil)
//     amount: raw bytes (0..16 bytes) (empty if nil)
//   - SatsPayment:     discriminator {2}
//     amount: uint64 big-endian (0 if nil)
//
// 6) memo: raw UTF-8 bytes (empty if nil)
// 7) sender_public_key: 33 bytes (0-filled if nil)
// 8) expiry_time (seconds): uint64 big-endian (0 if nil)
function HashSparkInvoiceV1(
  sparkInvoiceFields: SparkInvoiceFields,
  receiverPublicKey: Uint8Array,
  network: NetworkType,
) {
  if (!sparkInvoiceFields) {
    throw new Error("Missing sparkInvoiceFields");
  }
  if (!receiverPublicKey) {
    throw new Error("Receiver public key is required");
  }
  if (!network) {
    throw new Error("Network is required");
  }

  const { version, id, paymentType, memo, senderPublicKey, expiryTime } =
    sparkInvoiceFields;

  const allHashes: Uint8Array[] = [];

  const versionHashObj = sha256.create();
  const versionBytes = uint32be(version);
  versionHashObj.update(versionBytes);
  allHashes.push(versionHashObj.digest());

  const idHashObj = sha256.create();
  if (!id || id.length !== 16) {
    throw new Error("invoice id must be exactly 16 bytes");
  }
  idHashObj.update(id);
  allHashes.push(idHashObj.digest());

  const networkHashObj = sha256.create();
  hashNetworkMagicInto(networkHashObj, network);
  allHashes.push(networkHashObj.digest());

  const receiverPubKeyHashObj = sha256.create();
  if (!receiverPublicKey || receiverPublicKey.length !== 33) {
    throw new Error("receiver public key must be exactly 33 bytes");
  }
  receiverPubKeyHashObj.update(receiverPublicKey);
  allHashes.push(receiverPubKeyHashObj.digest());

  switch (paymentType?.$case) {
    case "tokensPayment": {
      const tp = paymentType.tokensPayment;
      // discriminator {1}
      const discrHash = sha256.create();
      discrHash.update(new Uint8Array([1]));
      allHashes.push(discrHash.digest());

      // token_identifier: 32 bytes (0-filled if nil)
      const tokenIdHash = sha256.create();
      const tokenIdentifier = tp.tokenIdentifier;
      if (!tokenIdentifier || tokenIdentifier.length === 0) {
        tokenIdHash.update(new Uint8Array(32));
      } else {
        if (tokenIdentifier.length !== 32) {
          throw new Error("token identifier must be exactly 32 bytes");
        }
        tokenIdHash.update(tokenIdentifier);
      }
      allHashes.push(tokenIdHash.digest());

      // amount: raw bytes (0..16 bytes) (empty if nil)
      const amountHash = sha256.create();
      const amount = tp.amount;
      if (amount && amount.length > 16) {
        throw new Error("token amount exceeds 16 bytes");
      }
      if (amount && amount.length > 0) {
        amountHash.update(amount);
      }
      allHashes.push(amountHash.digest());
      break;
    }
    case "satsPayment": {
      const sp = paymentType.satsPayment;
      // discriminator {2}
      const discrHash = sha256.create();
      discrHash.update(new Uint8Array([2]));
      allHashes.push(discrHash.digest());

      // amount: uint64 big-endian (0 if nil)
      const satsHash = sha256.create();
      let sats = 0n;
      if (sp && typeof sp.amount === "number" && sp.amount !== 0) {
        // amount is a number (sats). Convert to bigint seconds.
        sats = BigInt(sp.amount);
      }
      satsHash.update(uint64be(sats));
      allHashes.push(satsHash.digest());
      break;
    }
    default: {
      throw new Error("unsupported or missing payment type");
    }
  }

  const memoHashObj = sha256.create();
  if (memo != null) {
    memoHashObj.update(new TextEncoder().encode(memo));
  }
  allHashes.push(memoHashObj.digest());

  const senderPubKeyHashObj = sha256.create();
  const spk = senderPublicKey;
  if (!spk || spk.length === 0) {
    senderPubKeyHashObj.update(new Uint8Array(33));
  } else {
    if (spk.length !== 33) {
      throw new Error("sender public key must be exactly 33 bytes");
    }
    senderPubKeyHashObj.update(spk);
  }
  allHashes.push(senderPubKeyHashObj.digest());

  const expiryHashObj = sha256.create();
  let exp = 0n;
  if (expiryTime instanceof Date) {
    const seconds = Math.floor(expiryTime.getTime() / 1000);
    if (seconds > 0) {
      exp = BigInt(seconds);
    }
  }
  expiryHashObj.update(uint64be(exp));
  allHashes.push(expiryHashObj.digest());

  const finalHash = sha256.create();
  for (const hash of allHashes) {
    finalHash.update(hash);
  }
  return finalHash.digest();
}

function bitcoinNetworkIdentifierFromNetwork(network: NetworkType): number {
  switch (network) {
    case "MAINNET":
      return 0xd9b4bef9;
    case "LOCAL":
    case "REGTEST":
      return 0xdab5bffa;
    case "TESTNET":
      return 0x0709110b;
    case "SIGNET":
      return 0x40cf030a;
    default:
      throw new Error("invalid network");
  }
}

function hashNetworkMagicInto(
  hasher: ReturnType<typeof sha256.create>,
  network: NetworkType,
) {
  const magic = bitcoinNetworkIdentifierFromNetwork(network);
  const magicBE = uint32be(magic);
  hasher.update(sha256(magicBE));
}

// Big-endian uint32 bytes
function uint32be(n: number): Uint8Array {
  const b = new Uint8Array(4);
  new DataView(b.buffer).setUint32(0, n >>> 0, false);
  return b;
}

// Big-endian uint64 from bigint
function uint64be(value: bigint): Uint8Array {
  const b = new Uint8Array(8);
  new DataView(b.buffer).setBigUint64(0, value, false);
  return b;
}
