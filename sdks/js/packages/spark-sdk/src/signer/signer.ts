import {
  bytesToHex,
  bytesToNumberBE,
  hexToBytes,
} from "@noble/curves/abstract/utils";
import { schnorr, secp256k1 } from "@noble/curves/secp256k1";
import { HDKey } from "@scure/bip32";
import * as bip39 from "@scure/bip39";
import { wordlist } from "@scure/bip39/wordlists/english";
import { sha256 } from "@scure/btc-signer/utils";
import { Buffer } from "buffer";
import * as ecies from "eciesjs";
import { TreeNode } from "../proto/spark.js";
import { generateAdaptorFromSignature } from "../utils/adaptor-signature.js";
import { subtractPrivateKeys } from "../utils/keys.js";
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

globalThis.Buffer = Buffer;
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

  generateMnemonic(): Promise<string>;
  createSparkWalletFromMnemonic(mnemonic: string): Promise<string>;
  createSparkWalletFromSeed(seed: Uint8Array | string): Promise<string>;

  restoreSigningKeysFromLeafs(leafs: TreeNode[]): Promise<void>;
  getTrackedPublicKeys(): Promise<Uint8Array[]>;
  // Generates a new private key, and returns the public key
  generatePublicKey(hash?: Uint8Array): Promise<Uint8Array>;
  // Called when a public key is no longer needed
  removePublicKey(publicKey: Uint8Array): Promise<void>;
  getSchnorrPublicKey(publicKey: Uint8Array): Promise<Uint8Array>;

  signSchnorr(message: Uint8Array, publicKey: Uint8Array): Promise<Uint8Array>;

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
  getSspIdentityPublicKey(network: Network): Promise<Uint8Array>;

  hashRandomPrivateKey(): Promise<Uint8Array>;
  generateAdaptorFromSignature(signature: Uint8Array): Promise<{
    adaptorSignature: Uint8Array;
    adaptorPublicKey: Uint8Array;
  }>;

  getDepositSigningKey(): Promise<Uint8Array>;
}

class DefaultSparkSigner implements SparkSigner {
  private identityPrivateKey: HDKey | null = null;
  // <hex, hex>
  private publicKeyToPrivateKeyMap: Map<string, string> = new Map();

  private commitmentToNonceMap: Map<SigningCommitment, SigningNonce> =
    new Map();

  private deriveSigningKey(hash: Uint8Array): Uint8Array {
    if (!this.identityPrivateKey) {
      throw new Error("Private key is not set");
    }

    let amount = 0;
    for (let i = 0; i < 8; i++) {
      const view = new DataView(hash.buffer, i * 4, 4);
      amount += view.getUint32(0, false);
      amount = amount % 0x80000000;
    }
    const newPrivateKey = this.identityPrivateKey.deriveChild(
      Number(amount) + 0x80000000,
    ).privateKey;

    if (!newPrivateKey) {
      throw new Error("Failed to recover signing key");
    }

    return newPrivateKey;
  }

  async restoreSigningKeysFromLeafs(leafs: TreeNode[]) {
    if (!this.identityPrivateKey) {
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

  async getIdentityPublicKey(): Promise<Uint8Array> {
    if (!this.identityPrivateKey?.privateKey) {
      throw new Error("Private key is not set");
    }

    return secp256k1.getPublicKey(this.identityPrivateKey.privateKey);
  }

  async generateMnemonic(): Promise<string> {
    return bip39.generateMnemonic(wordlist);
  }

  async createSparkWalletFromMnemonic(mnemonic: string): Promise<string> {
    const seed = await bip39.mnemonicToSeed(mnemonic);
    return this.createSparkWalletFromSeed(seed);
  }

  async getTrackedPublicKeys(): Promise<Uint8Array[]> {
    return Array.from(this.publicKeyToPrivateKeyMap.keys()).map(hexToBytes);
  }

  async generatePublicKey(hash?: Uint8Array): Promise<Uint8Array> {
    if (!this.identityPrivateKey) {
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

  async createSparkWalletFromSeed(seed: Uint8Array): Promise<string> {
    if (typeof seed === "string") {
      seed = hexToBytes(seed);
    }

    const hdkey = HDKey.fromMasterSeed(seed).derive("m/0");

    if (!hdkey.privateKey) {
      throw new Error("Could not derive private key from seed");
    }

    this.identityPrivateKey = hdkey;

    return bytesToHex(secp256k1.getPublicKey(hdkey.privateKey, true));
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
    if (!this.identityPrivateKey?.privateKey) {
      throw new Error("Private key is not set");
    }

    const signature = secp256k1.sign(
      message,
      this.identityPrivateKey.privateKey,
    );

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
    if (!this.identityPrivateKey?.privateKey) {
      throw new Error("Private key is not set");
    }
    const receiverEciesPrivKey = ecies.PrivateKey.fromHex(
      bytesToHex(this.identityPrivateKey.privateKey),
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

  // Hardcode this for default ssp
  async getSspIdentityPublicKey(network: Network): Promise<Uint8Array> {
    if (network === Network.MAINNET) {
      return hexToBytes(
        "02e0b8d42c5d3b5fe4c5beb6ea796ab3bc8aaf28a3d3195407482c67e0b58228a5",
      );
    } else {
      return hexToBytes(
        "028c094a432d46a0ac95349d792c2e3730bd60c29188db716f56a99e39b95338b4",
      );
    }
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

  async getDepositSigningKey(): Promise<Uint8Array> {
    if (!this.identityPrivateKey?.privateKey) {
      throw new Error("Private key is not set");
    }

    const depositSigningKey =
      this.identityPrivateKey.derive("M/8797555'/0'/2'");

    if (!depositSigningKey.privateKey) {
      throw new Error("Could not derive deposit signing key");
    }

    this.publicKeyToPrivateKeyMap.set(
      bytesToHex(secp256k1.getPublicKey(depositSigningKey.privateKey)),
      bytesToHex(depositSigningKey.privateKey),
    );

    return secp256k1.getPublicKey(depositSigningKey.privateKey);
  }
}
export { DefaultSparkSigner };
export type { SparkSigner };
