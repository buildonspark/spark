import {
  bytesToHex,
  bytesToNumberBE,
  hexToBytes,
} from "@noble/curves/abstract/utils";
import { schnorr, secp256k1 } from "@noble/curves/secp256k1";
import { HDKey } from "@scure/bip32";
import * as bip39 from "@scure/bip39";
import { generateMnemonic } from "@scure/bip39";
import { wordlist } from "@scure/bip39/wordlists/english";
import { sha256 } from "@scure/btc-signer/utils";
import * as ecies from "eciesjs";
import { TreeNode } from "../proto/spark.js";
import { generateAdaptorFromSignature } from "../utils/adaptor-signature.js";
import { getMasterHDKeyFromSeed, subtractPrivateKeys } from "../utils/keys.js";
import { Network } from "../utils/network.js";
import {
  splitSecretWithProofs,
  VerifiableSecretShare,
} from "../utils/secret-sharing.js";
import {
  createWasmSigningCommitment,
  createWasmSigningNonce,
  getRandomSigningNonce,
  getSigningCommitmentFromNonce,
} from "../utils/signing.js";
import { aggregateFrost, signFrost } from "../utils/wasm.js";
import { KeyPackage } from "../wasm/spark_bindings.js";

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
  curveOrder: bigint;
  threshold: number;
  numShares: number;
  isSecretPubkey?: boolean;
};

// TODO: Properly clean up keys when they are no longer needed
interface SparkSigner {
  getIdentityPublicKey(): Promise<Uint8Array>;
  getDepositSigningKey(): Promise<Uint8Array>;

  generateMnemonic(): Promise<string>;
  mnemonicToSeed(mnemonic: string): Promise<Uint8Array>;

  createSparkWalletFromSeed(
    seed: Uint8Array | string,
    network: Network,
  ): Promise<string>;

  restoreSigningKeysFromLeafs(leafs: TreeNode[]): Promise<void>;
  getTrackedPublicKeys(): Promise<Uint8Array[]>;
  // Generates a new private key, and returns the public key
  generatePublicKey(hash?: Uint8Array): Promise<Uint8Array>;
  // Called when a public key is no longer needed
  removePublicKey(publicKey: Uint8Array): Promise<void>;
  getSchnorrPublicKey(publicKey: Uint8Array): Promise<Uint8Array>;

  signSchnorr(message: Uint8Array, publicKey: Uint8Array): Promise<Uint8Array>;
  signSchnorrWithIdentityKey(message: Uint8Array): Promise<Uint8Array>;

  subtractPrivateKeysGivenPublicKeys(
    first: Uint8Array,
    second: Uint8Array,
  ): Promise<Uint8Array>;
  splitSecretWithProofs(
    params: SplitSecretWithProofsParams,
  ): Promise<VerifiableSecretShare[]>;

  signFrost(params: SignFrostParams): Promise<Uint8Array>;
  aggregateFrost(params: AggregateFrostParams): Promise<Uint8Array>;

  signMessageWithPublicKey(
    message: Uint8Array,
    publicKey: Uint8Array,
    compact?: boolean,
  ): Promise<Uint8Array>;
  // If compact is true, the signature should be in ecdsa compact format else it should be in DER format
  signMessageWithIdentityKey(
    message: Uint8Array,
    compact?: boolean,
  ): Promise<Uint8Array>;

  encryptLeafPrivateKeyEcies(
    receiverPublicKey: Uint8Array,
    publicKey: Uint8Array,
  ): Promise<Uint8Array>;
  decryptEcies(ciphertext: Uint8Array): Promise<Uint8Array>;

  getRandomSigningCommitment(): Promise<SigningCommitment>;

  hashRandomPrivateKey(): Promise<Uint8Array>;
  generateAdaptorFromSignature(signature: Uint8Array): Promise<{
    adaptorSignature: Uint8Array;
    adaptorPublicKey: Uint8Array;
  }>;

  getDepositSigningKey(): Promise<Uint8Array>;
  getMasterPublicKey(): Promise<Uint8Array>;
}

class DefaultSparkSigner implements SparkSigner {
  private masterKey: HDKey | null = null;
  private identityKey: HDKey | null = null;
  private signingKey: HDKey | null = null;
  private depositKey: HDKey | null = null;

  // <hex, hex>
  private publicKeyToPrivateKeyMap: Map<string, string> = new Map();

  private commitmentToNonceMap: Map<SigningCommitment, SigningNonce> =
    new Map();

  private deriveSigningKey(hash: Uint8Array): Uint8Array {
    if (!this.masterKey) {
      throw new Error("Private key is not set");
    }

    const view = new DataView(hash.buffer);
    const amount = (view.getUint32(0, false) % 0x80000000) + 0x80000000;

    const newPrivateKey = this.signingKey?.deriveChild(amount).privateKey;

    if (!newPrivateKey) {
      throw new Error("Failed to recover signing key");
    }

    return newPrivateKey;
  }

  async restoreSigningKeysFromLeafs(leafs: TreeNode[]) {
    if (!this.masterKey) {
      throw new Error("Private key is not set");
    }

    for (const leaf of leafs) {
      const hash = sha256(leaf.id);
      const privateKey = this.deriveSigningKey(hash);

      const publicKey = secp256k1.getPublicKey(privateKey);
      this.publicKeyToPrivateKeyMap.set(
        bytesToHex(publicKey),
        bytesToHex(privateKey),
      );
    }
  }

  async getSchnorrPublicKey(publicKey: Uint8Array): Promise<Uint8Array> {
    const privateKey = this.publicKeyToPrivateKeyMap.get(bytesToHex(publicKey));
    if (!privateKey) {
      throw new Error("Private key is not set");
    }

    return schnorr.getPublicKey(hexToBytes(privateKey));
  }

  async signSchnorr(
    message: Uint8Array,
    publicKey: Uint8Array,
  ): Promise<Uint8Array> {
    const privateKey = this.publicKeyToPrivateKeyMap.get(bytesToHex(publicKey));
    if (!privateKey) {
      throw new Error("Private key is not set");
    }

    return schnorr.sign(message, hexToBytes(privateKey));
  }

  async signSchnorrWithIdentityKey(message: Uint8Array): Promise<Uint8Array> {
    if (!this.identityKey?.privateKey) {
      throw new Error("Private key is not set");
    }

    const signature = schnorr.sign(message, this.identityKey.privateKey);

    return signature;
  }

  async getIdentityPublicKey(): Promise<Uint8Array> {
    if (!this.identityKey?.publicKey) {
      throw new Error("Private key is not set");
    }

    return this.identityKey.publicKey;
  }

  async getDepositSigningKey(): Promise<Uint8Array> {
    if (!this.depositKey?.publicKey) {
      throw new Error("Deposit key is not set");
    }

    return this.depositKey.publicKey;
  }

  async generateMnemonic(): Promise<string> {
    return generateMnemonic(wordlist);
  }

  async mnemonicToSeed(mnemonic: string): Promise<Uint8Array> {
    return await bip39.mnemonicToSeed(mnemonic);
  }

  async getTrackedPublicKeys(): Promise<Uint8Array[]> {
    return Array.from(this.publicKeyToPrivateKeyMap.keys()).map(hexToBytes);
  }

  async generatePublicKey(hash?: Uint8Array): Promise<Uint8Array> {
    if (!this.masterKey) {
      throw new Error("Private key is not set");
    }

    let newPrivateKey: Uint8Array | null = null;
    if (hash) {
      newPrivateKey = this.deriveSigningKey(hash);
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

  async removePublicKey(publicKey: Uint8Array): Promise<void> {
    this.publicKeyToPrivateKeyMap.delete(bytesToHex(publicKey));
  }

  async subtractPrivateKeysGivenPublicKeys(
    first: Uint8Array,
    second: Uint8Array,
  ): Promise<Uint8Array> {
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
      secondPrivateKey,
    );
    const resultPubKey = secp256k1.getPublicKey(resultPrivKey);

    const resultPrivKeyHex = bytesToHex(resultPrivKey);
    const resultPubKeyHex = bytesToHex(resultPubKey);
    this.publicKeyToPrivateKeyMap.set(resultPubKeyHex, resultPrivKeyHex);
    return resultPubKey;
  }

  async splitSecretWithProofs({
    secret,
    curveOrder,
    threshold,
    numShares,
    isSecretPubkey = false,
  }: SplitSecretWithProofsParams): Promise<VerifiableSecretShare[]> {
    if (isSecretPubkey) {
      const pubKeyHex = bytesToHex(secret);
      const privateKey = this.publicKeyToPrivateKeyMap.get(pubKeyHex);
      if (!privateKey) {
        throw new Error("Private key is not set");
      }
      secret = hexToBytes(privateKey);
    }
    const secretAsInt = bytesToNumberBE(secret);
    return splitSecretWithProofs(secretAsInt, curveOrder, threshold, numShares);
  }

  async signFrost({
    message,
    privateAsPubKey,
    publicKey,
    verifyingKey,
    selfCommitment,
    statechainCommitments,
    adaptorPubKey,
  }: SignFrostParams): Promise<Uint8Array> {
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
      verifyingKey,
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

  async aggregateFrost({
    message,
    publicKey,
    verifyingKey,
    selfCommitment,
    statechainCommitments,
    adaptorPubKey,
    selfSignature,
    statechainSignatures,
    statechainPublicKeys,
  }: AggregateFrostParams): Promise<Uint8Array> {
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

  async createSparkWalletFromSeed(
    seed: Uint8Array | string,
    network: Network,
  ): Promise<string> {
    if (typeof seed === "string") {
      seed = hexToBytes(seed);
    }

    const hdkey = getMasterHDKeyFromSeed(seed);

    if (!hdkey.privateKey || !hdkey.publicKey) {
      throw new Error("Could not derive private key from seed");
    }

    const accountType = network === Network.REGTEST ? 0 : 1;
    const identityKey = hdkey.derive(`m/8797555'/${accountType}'/0'`);
    const signingKey = hdkey.derive(`m/8797555'/${accountType}'/1'`);
    const depositKey = hdkey.derive(`m/8797555'/${accountType}'/2'`);

    if (
      !identityKey.privateKey ||
      !depositKey.privateKey ||
      !signingKey.privateKey ||
      !identityKey.publicKey ||
      !depositKey.publicKey ||
      !signingKey.publicKey
    ) {
      throw new Error("Could not derive keys from seed");
    }

    this.masterKey = hdkey;
    this.identityKey = identityKey;
    this.depositKey = depositKey;
    this.signingKey = signingKey;

    this.publicKeyToPrivateKeyMap.set(
      bytesToHex(identityKey.publicKey),
      bytesToHex(identityKey.privateKey),
    );
    this.publicKeyToPrivateKeyMap.set(
      bytesToHex(depositKey.publicKey),
      bytesToHex(depositKey.privateKey),
    );

    return bytesToHex(identityKey.publicKey);
  }

  async signMessageWithPublicKey(
    message: Uint8Array,
    publicKey: Uint8Array,
    compact?: boolean,
  ): Promise<Uint8Array> {
    const privateKey = this.publicKeyToPrivateKeyMap.get(bytesToHex(publicKey));
    if (!privateKey) {
      throw new Error(
        `No private key found for public key: ${bytesToHex(publicKey)}`,
      );
    }

    const signature = secp256k1.sign(message, hexToBytes(privateKey));

    if (compact) {
      return signature.toCompactRawBytes();
    }

    return signature.toDERRawBytes();
  }

  async signMessageWithIdentityKey(
    message: Uint8Array,
    compact?: boolean,
  ): Promise<Uint8Array> {
    if (!this.identityKey?.privateKey) {
      throw new Error("Private key is not set");
    }

    const signature = secp256k1.sign(message, this.identityKey.privateKey);

    if (compact) {
      return signature.toCompactRawBytes();
    }

    return signature.toDERRawBytes();
  }

  async encryptLeafPrivateKeyEcies(
    receiverPublicKey: Uint8Array,
    publicKey: Uint8Array,
  ): Promise<Uint8Array> {
    const publicKeyHex = bytesToHex(publicKey);
    const privateKey = this.publicKeyToPrivateKeyMap.get(publicKeyHex);
    if (!privateKey) {
      throw new Error("Private key is not set");
    }

    return ecies.encrypt(receiverPublicKey, hexToBytes(privateKey));
  }

  async decryptEcies(ciphertext: Uint8Array): Promise<Uint8Array> {
    if (!this.identityKey?.privateKey) {
      throw new Error("Private key is not set");
    }
    const receiverEciesPrivKey = ecies.PrivateKey.fromHex(
      bytesToHex(this.identityKey.privateKey),
    );
    const privateKey = ecies.decrypt(receiverEciesPrivKey.toHex(), ciphertext);
    const publicKey = secp256k1.getPublicKey(privateKey);
    const publicKeyHex = bytesToHex(publicKey);
    const privateKeyHex = bytesToHex(privateKey);
    this.publicKeyToPrivateKeyMap.set(publicKeyHex, privateKeyHex);
    return publicKey;
  }

  async getRandomSigningCommitment(): Promise<SigningCommitment> {
    const nonce = getRandomSigningNonce();
    const commitment = getSigningCommitmentFromNonce(nonce);
    this.commitmentToNonceMap.set(commitment, nonce);
    return commitment;
  }

  async hashRandomPrivateKey(): Promise<Uint8Array> {
    return sha256(secp256k1.utils.randomPrivateKey());
  }

  async generateAdaptorFromSignature(signature: Uint8Array): Promise<{
    adaptorSignature: Uint8Array;
    adaptorPublicKey: Uint8Array;
  }> {
    const adaptor = generateAdaptorFromSignature(signature);

    const adaptorPublicKey = secp256k1.getPublicKey(adaptor.adaptorPrivateKey);

    this.publicKeyToPrivateKeyMap.set(
      bytesToHex(adaptorPublicKey),
      bytesToHex(adaptor.adaptorPrivateKey),
    );

    return {
      adaptorSignature: signature,
      adaptorPublicKey: adaptorPublicKey,
    };
  }

  async getMasterPublicKey(): Promise<Uint8Array> {
    if (!this.masterKey?.publicKey) {
      throw new Error("Private key is not set");
    }

    return this.masterKey.publicKey;
  }
}
export { DefaultSparkSigner };
export type { SparkSigner };
