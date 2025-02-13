import {
  bytesToHex,
  bytesToNumberBE,
  hexToBytes,
} from "@noble/curves/abstract/utils";
import { schnorr, secp256k1 } from "@noble/curves/secp256k1";
import { HDKey } from "@scure/bip32";
import * as bip39 from "@scure/bip39";
import { wordlist } from "@scure/bip39/wordlists/english";
import assert from "assert";
import * as ecies from "eciesjs";
import { subtractPrivateKeys } from "../utils/keys";
import {
  splitSecretWithProofs,
  VerifiableSecretShare,
} from "../utils/secret-sharing";
import {
  createWasmSigningCommitment,
  createWasmSigningNonce,
  getRandomSigningNonce,
  getSigningCommitmentFromNonce,
} from "../utils/signing";
import { aggregateFrost, signFrost } from "../utils/wasm";
import { KeyPackage } from "../wasm/spark_bindings";

export type SigningNonce = {
  binding: Uint8Array;
  hiding: Uint8Array;
};

export type SigningCommitment = {
  binding: Uint8Array;
  hiding: Uint8Array;
};

export type SignFrostParams = {
  message: Uint8Array;
  privateAsPubKey: Uint8Array;
  publicKey: Uint8Array;
  verifyingKey: Uint8Array;
  selfCommitment: SigningCommitment;
  statechainCommitments?: { [key: string]: SigningCommitment } | undefined;
  adaptorPubKey?: Uint8Array | undefined;
};

export type AggregateFrostParams = Omit<SignFrostParams, "privateAsPubKey"> & {
  selfSignature: Uint8Array;
  statechainSignatures?: { [key: string]: Uint8Array } | undefined;
  statechainPublicKeys?: { [key: string]: Uint8Array } | undefined;
};

export type SplitSecretWithProofsParams = {
  secret: Uint8Array;
  isSecretPubkey: boolean;
  curveOrder: bigint;
  threshold: number;
  numShares: number;
};

// TODO: Properly clean up keys when they are no longer needed
interface SparkSigner {
  getIdentityPublicKey(): Uint8Array;

  generateMnemonic(): string;
  createSparkWalletFromMnemonic(mnemonic: string): Promise<string>;
  createSparkWalletFromSeed(seed: Uint8Array | string): string;

  // Generates a new private key, and returns the public key
  generatePublicKey(hash?: Uint8Array): Uint8Array;
  // Called when a public key is no longer needed
  removePublicKey(publicKey: Uint8Array): void;
  getSchnorrPublicKey(publicKey: Uint8Array): Uint8Array;

  signSchnorr(message: Uint8Array, publicKey: Uint8Array): Uint8Array;

  subtractPrivateKeysGivenPublicKeys(
    first: Uint8Array,
    second: Uint8Array
  ): Uint8Array;
  splitSecretWithProofs(
    params: SplitSecretWithProofsParams
  ): VerifiableSecretShare[];

  signFrost(params: SignFrostParams): Uint8Array;
  aggregateFrost(params: AggregateFrostParams): Uint8Array;

  signEcdsaWithIdentityPrivateKey(message: Uint8Array): Uint8Array;
  encryptLeafPrivateKeyEcies(
    receiverPublicKey: Uint8Array,
    publicKey: Uint8Array
  ): Uint8Array;
  decryptEcies(ciphertext: Uint8Array): Uint8Array;

  getRandomSigningCommitment(): SigningCommitment;
  getSspIdentityPublicKey(): Uint8Array;
}

class DefaultSparkSigner implements SparkSigner {
  private identityPrivateKey: HDKey | null = null;
  // <hex, hex>
  private publicKeyToPrivateKeyMap: Map<string, string> = new Map();

  private commitmentToNonceMap: Map<SigningCommitment, SigningNonce> =
    new Map();

  getSchnorrPublicKey(publicKey: Uint8Array): Uint8Array {
    const privateKey = this.publicKeyToPrivateKeyMap.get(bytesToHex(publicKey));
    if (!privateKey) {
      throw new Error("Private key is not set");
    }

    return schnorr.getPublicKey(hexToBytes(privateKey));
  }

  signSchnorr(message: Uint8Array, publicKey: Uint8Array): Uint8Array {
    const privateKey = this.publicKeyToPrivateKeyMap.get(bytesToHex(publicKey));
    if (!privateKey) {
      throw new Error("Private key is not set");
    }

    return schnorr.sign(message, hexToBytes(privateKey));
  }

  getIdentityPublicKey(): Uint8Array {
    if (!this.identityPrivateKey?.privateKey) {
      throw new Error("Private key is not set");
    }

    return secp256k1.getPublicKey(this.identityPrivateKey.privateKey);
  }

  generateMnemonic(): string {
    return bip39.generateMnemonic(wordlist);
  }

  async createSparkWalletFromMnemonic(mnemonic: string): Promise<string> {
    const seed = await bip39.mnemonicToSeed(mnemonic);
    return this.createSparkWalletFromSeed(seed);
  }

  generatePublicKey(hash?: Uint8Array): Uint8Array {
    if (!this.identityPrivateKey) {
      throw new Error("Private key is not set");
    }

    let newPrivateKey: Uint8Array | null = null;
    let amount = 0n;
    if (hash) {
      for (let i = 0; i < 8; i++) {
        amount += bytesToNumberBE(hash.slice(i * 4, i * 4 + 4));
        amount = amount % (2n ** 32n - 1n);
      }
      newPrivateKey = this.identityPrivateKey.deriveChild(
        Number(amount)
      ).privateKey;
    } else {
      newPrivateKey = secp256k1.utils.randomPrivateKey();
    }

    if (!newPrivateKey) {
      throw new Error("Failed to generate new private key");
    }

    const publicKey = secp256k1.getPublicKey(newPrivateKey);

    const pubKeyHex = bytesToHex(publicKey);
    const privKeyHex = bytesToHex(newPrivateKey);
    this.publicKeyToPrivateKeyMap.set(pubKeyHex, privKeyHex);

    return publicKey;
  }

  removePublicKey(publicKey: Uint8Array): void {
    this.publicKeyToPrivateKeyMap.delete(bytesToHex(publicKey));
  }

  subtractPrivateKeysGivenPublicKeys(
    first: Uint8Array,
    second: Uint8Array
  ): Uint8Array {
    const firstPubKeyHex = bytesToHex(first);
    const secondPubKeyHex = bytesToHex(second);

    const firstPrivateKeyHex =
      this.publicKeyToPrivateKeyMap.get(firstPubKeyHex);
    const secondPrivateKeyHex =
      this.publicKeyToPrivateKeyMap.get(secondPubKeyHex);

    if (!firstPrivateKeyHex || !secondPrivateKeyHex) {
      throw new Error("Private key is not set");
    }

    const firstPrivateKey = hexToBytes(firstPrivateKeyHex);
    const secondPrivateKey = hexToBytes(secondPrivateKeyHex);

    const resultPrivKey = subtractPrivateKeys(
      firstPrivateKey,
      secondPrivateKey
    );
    const resultPubKey = secp256k1.getPublicKey(resultPrivKey);

    const resultPrivKeyHex = bytesToHex(resultPrivKey);
    const resultPubKeyHex = bytesToHex(resultPubKey);
    this.publicKeyToPrivateKeyMap.set(resultPubKeyHex, resultPrivKeyHex);
    return resultPubKey;
  }

  splitSecretWithProofs({
    secret,
    isSecretPubkey,
    curveOrder,
    threshold,
    numShares,
  }: SplitSecretWithProofsParams): VerifiableSecretShare[] {
    if (isSecretPubkey) {
      const secretPrivKey = this.publicKeyToPrivateKeyMap.get(
        bytesToHex(secret)
      );
      if (!secretPrivKey) {
        throw new Error("Private key is not set");
      }
      secret = hexToBytes(secretPrivKey);
    }

    const secretAsInt = bytesToNumberBE(secret);
    return splitSecretWithProofs(secretAsInt, curveOrder, threshold, numShares);
  }

  signFrost({
    message,
    privateAsPubKey,
    publicKey,
    verifyingKey,
    selfCommitment,
    statechainCommitments,
    adaptorPubKey,
  }: SignFrostParams): Uint8Array {
    const privateAsPubKeyHex = bytesToHex(privateAsPubKey);
    const signingPrivateKey =
      this.publicKeyToPrivateKeyMap.get(privateAsPubKeyHex);

    if (!signingPrivateKey) {
      throw new Error("Private key is not set");
    }

    const nonce = this.commitmentToNonceMap.get(selfCommitment);
    if (!nonce) {
      throw new Error("Nonce is not set");
    }

    const keyPackage = new KeyPackage(
      hexToBytes(signingPrivateKey),
      publicKey,
      verifyingKey
    );

    return signFrost({
      msg: message,
      keyPackage,
      nonce: createWasmSigningNonce(nonce),
      selfCommitment: createWasmSigningCommitment(selfCommitment),
      statechainCommitments,
      adaptorPubKey,
    });
  }

  aggregateFrost({
    message,
    publicKey,
    verifyingKey,
    selfCommitment,
    statechainCommitments,
    adaptorPubKey,
    selfSignature,
    statechainSignatures,
    statechainPublicKeys,
  }: AggregateFrostParams): Uint8Array {
    return aggregateFrost({
      msg: message,
      statechainSignatures,
      statechainPublicKeys,
      verifyingKey,
      statechainCommitments,
      selfCommitment: createWasmSigningCommitment(selfCommitment),
      selfPublicKey: publicKey,
      selfSignature,
      adaptorPubKey,
    });
  }

  createSparkWalletFromSeed(seed: Uint8Array): string {
    if (typeof seed === "string") {
      seed = hexToBytes(seed);
    }

    const hdkey = HDKey.fromMasterSeed(seed);

    assert(hdkey.privateKey, "Private key is not set");

    this.identityPrivateKey = hdkey;

    return bytesToHex(secp256k1.getPublicKey(hdkey.privateKey, true));
  }

  signEcdsaWithIdentityPrivateKey(message: Uint8Array): Uint8Array {
    if (!this.identityPrivateKey?.privateKey) {
      throw new Error("Private key is not set");
    }

    return secp256k1
      .sign(message, this.identityPrivateKey.privateKey)
      .toCompactRawBytes();
  }

  encryptLeafPrivateKeyEcies(
    receiverPublicKey: Uint8Array,
    publicKey: Uint8Array
  ): Uint8Array {
    const publicKeyHex = bytesToHex(publicKey);
    const privateKey = this.publicKeyToPrivateKeyMap.get(publicKeyHex);
    if (!privateKey) {
      throw new Error("Private key is not set");
    }

    return ecies.encrypt(receiverPublicKey, hexToBytes(privateKey));
  }

  decryptEcies(ciphertext: Uint8Array): Uint8Array {
    if (!this.identityPrivateKey?.privateKey) {
      throw new Error("Private key is not set");
    }

    const receiverEciesPrivKey = ecies.PrivateKey.fromHex(
      bytesToHex(this.identityPrivateKey.privateKey)
    );

    const privateKey = ecies.decrypt(receiverEciesPrivKey.toHex(), ciphertext);
    const publicKey = secp256k1.getPublicKey(privateKey);

    const publicKeyHex = bytesToHex(publicKey);
    const privateKeyHex = bytesToHex(privateKey);
    this.publicKeyToPrivateKeyMap.set(publicKeyHex, privateKeyHex);
    return publicKey;
  }

  getRandomSigningCommitment(): SigningCommitment {
    const nonce = getRandomSigningNonce();
    const commitment = getSigningCommitmentFromNonce(nonce);
    this.commitmentToNonceMap.set(commitment, nonce);
    return commitment;
  }

  // Hardcode this for default ssp
  getSspIdentityPublicKey(): Uint8Array {
    return hexToBytes(
      "030868bb1892292e7e4cd6c14a02c16ca2326f07a185d45b2f1068d996532559d5"
    );
  }
}

export { DefaultSparkSigner, SparkSigner };
