import {
  bytesToHex,
  bytesToNumberBE,
  equalBytes,
  hexToBytes,
} from "@noble/curves/abstract/utils";
import { schnorr, secp256k1 } from "@noble/curves/secp256k1";
import { HDKey } from "@scure/bip32";
import { generateMnemonic, mnemonicToSeed } from "@scure/bip39";
import { wordlist } from "@scure/bip39/wordlists/english";
import * as ecies from "eciesjs";
import { isReactNative } from "../constants.js";
import { ConfigurationError, ValidationError } from "../errors/types.js";
import { TreeNode } from "../proto/spark.js";
import { IKeyPackage, ISigningCommitment } from "../spark_bindings/types.js";
import { generateAdaptorFromSignature } from "../utils/adaptor-signature.js";
import { subtractPrivateKeys } from "../utils/keys.js";
import {
  splitSecretWithProofs,
  VerifiableSecretShare,
} from "../utils/secret-sharing.js";
import {
  getRandomSigningNonce,
  getSigningCommitmentFromNonce,
} from "../utils/signing.js";

let sparkFrostModule: any = undefined;
const getSparkFrostModule = async () => {
  if (isReactNative) {
    return undefined;
  }
  if (!sparkFrostModule) {
    // Use dynamic import
    sparkFrostModule = await import("../spark_bindings/wasm/index.js");
  }
  return sparkFrostModule;
};

import { privateAdd, privateNegate } from "@bitcoinerlab/secp256k1";
import {
  fromPrivateKey,
  PARITY,
  Receipt,
  TokenSigner,
} from "@buildonspark/lrc20-sdk";
import { sha256 } from "@noble/hashes/sha2";
import { Transaction } from "@scure/btc-signer";
import type { Psbt } from "bitcoinjs-lib";

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
  selfCommitment: ISigningCommitment;
  statechainCommitments?: { [key: string]: ISigningCommitment } | undefined;
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

type DerivedHDKey = {
  hdKey: HDKey;
  privateKey: Uint8Array;
  publicKey: Uint8Array;
};

type KeyPair = {
  privateKey: Uint8Array;
  publicKey: Uint8Array;
};

interface SparkKeyGenerator {
  deriveHDKeysFromSeed(
    seed: Uint8Array,
    accountNumber: number,
  ): Promise<{
    masterPublicKey: Uint8Array;
    identityKey: KeyPair;
    signingHDKey: DerivedHDKey;
    depositKey: KeyPair;
    staticDepositHDKey: DerivedHDKey;
  }>;
}

const HARDENED_OFFSET = 0x80000000; // 2^31

class DefaultSparkKeyGenerator implements SparkKeyGenerator {
  async deriveHDKeysFromSeed(
    seed: Uint8Array,
    accountNumber: number,
  ): Promise<{
    masterPublicKey: Uint8Array;
    identityKey: KeyPair;
    signingHDKey: DerivedHDKey;
    depositKey: KeyPair;
    staticDepositHDKey: DerivedHDKey;
  }> {
    const hdkey = HDKey.fromMasterSeed(seed);

    if (!hdkey.privateKey || !hdkey.publicKey) {
      throw new ValidationError("Failed to derive keys from seed", {
        field: "hdkey",
        value: seed,
      });
    }

    const identityKey = hdkey.derive(`m/8797555'/${accountNumber}'/0'`);
    const signingKey = hdkey.derive(`m/8797555'/${accountNumber}'/1'`);
    const depositKey = hdkey.derive(`m/8797555'/${accountNumber}'/2'`);
    const staticDepositKey = hdkey.derive(`m/8797555'/${accountNumber}'/3'`);

    if (
      !identityKey.privateKey ||
      !depositKey.privateKey ||
      !signingKey.privateKey ||
      !identityKey.publicKey ||
      !depositKey.publicKey ||
      !signingKey.publicKey ||
      !staticDepositKey.privateKey ||
      !staticDepositKey.publicKey
    ) {
      throw new ValidationError(
        "Failed to derive all required keys from seed",
        {
          field: "derivedKeys",
        },
      );
    }

    return {
      masterPublicKey: hdkey.publicKey,
      identityKey: {
        privateKey: identityKey.privateKey,
        publicKey: identityKey.publicKey,
      },
      signingHDKey: {
        hdKey: signingKey,
        privateKey: signingKey.privateKey,
        publicKey: signingKey.publicKey,
      },
      depositKey: {
        privateKey: depositKey.privateKey,
        publicKey: depositKey.publicKey,
      },
      staticDepositHDKey: {
        hdKey: staticDepositKey,
        privateKey: staticDepositKey.privateKey,
        publicKey: staticDepositKey.publicKey,
      },
    };
  }
}

// TODO: Properly clean up keys when they are no longer needed
interface SparkSigner extends TokenSigner {
  getIdentityPublicKey(): Promise<Uint8Array>;
  getDepositSigningKey(): Promise<Uint8Array>;
  generateStaticDepositKey(idx: number): Promise<Uint8Array>;
  getStaticDepositSigningKey(idx: number): Promise<Uint8Array>;
  getStaticDepositSecretKey(idx: number): Promise<Uint8Array>;

  generateMnemonic(): Promise<string>;
  mnemonicToSeed(mnemonic: string): Promise<Uint8Array>;

  createSparkWalletFromSeed(
    seed: Uint8Array | string,
    accountNumber?: number,
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
  validateMessageWithIdentityKey(
    message: Uint8Array,
    signature: Uint8Array,
  ): Promise<boolean>;

  signTransactionIndex(
    tx: Transaction,
    index: number,
    publicKey: Uint8Array,
  ): void;

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
  private masterPublicKey: Uint8Array | null = null;
  private identityKey: KeyPair | null = null;
  private signingKey: HDKey | null = null;
  private depositKey: KeyPair | null = null;
  private staticDepositKey: HDKey | null = null;
  private staticDepositKeyMap: Map<number, HDKey> = new Map();

  // <hex, hex>
  protected publicKeyToPrivateKeyMap: Map<string, string> = new Map();

  protected commitmentToNonceMap: Map<SigningCommitment, SigningNonce> =
    new Map();

  private readonly keyGenerator: SparkKeyGenerator;

  constructor({ hdKeyGenerator }: { hdKeyGenerator?: SparkKeyGenerator } = {}) {
    this.keyGenerator = hdKeyGenerator ?? new DefaultSparkKeyGenerator();
  }

  private deriveSigningKey(hash: Uint8Array): Uint8Array {
    if (!this.signingKey) {
      throw new ValidationError("Private key not initialized", {
        field: "signingKey",
      });
    }

    const view = new DataView(hash.buffer);
    const amount =
      (view.getUint32(0, false) % HARDENED_OFFSET) + HARDENED_OFFSET;

    const newPrivateKey = this.signingKey?.deriveChild(amount).privateKey;

    if (!newPrivateKey) {
      throw new ValidationError("Failed to recover signing key", {
        field: "privateKey",
      });
    }

    return newPrivateKey;
  }

  async restoreSigningKeysFromLeafs(leafs: TreeNode[]) {
    if (!this.signingKey) {
      throw new ValidationError("Signing key is not set", {
        field: "signingKey",
      });
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
      throw new ValidationError("Private key is not set", {
        field: "privateKey",
      });
    }

    return schnorr.getPublicKey(hexToBytes(privateKey));
  }

  async signSchnorr(
    message: Uint8Array,
    publicKey: Uint8Array,
  ): Promise<Uint8Array> {
    const privateKey = this.publicKeyToPrivateKeyMap.get(bytesToHex(publicKey));
    if (!privateKey) {
      throw new ValidationError("Private key is not set", {
        field: "privateKey",
      });
    }

    return schnorr.sign(message, hexToBytes(privateKey));
  }

  async signSchnorrWithIdentityKey(message: Uint8Array): Promise<Uint8Array> {
    if (!this.identityKey?.privateKey) {
      throw new ValidationError("Private key not set", {
        field: "identityKey",
      });
    }

    const signature = schnorr.sign(message, this.identityKey.privateKey);

    return signature;
  }

  async getIdentityPublicKey(): Promise<Uint8Array> {
    if (!this.identityKey?.publicKey) {
      throw new ValidationError("Private key is not set", {
        field: "identityKey",
      });
    }

    return this.identityKey.publicKey;
  }

  async getDepositSigningKey(): Promise<Uint8Array> {
    if (!this.depositKey?.publicKey) {
      throw new ValidationError("Deposit key is not set", {
        field: "depositKey",
      });
    }

    return this.depositKey.publicKey;
  }

  async generateStaticDepositKey(idx: number): Promise<Uint8Array> {
    if (!this.staticDepositKey?.privateKey) {
      throw new ValidationError("Static deposit key is not set", {
        field: "staticDepositKey",
      });
    }

    if (this.staticDepositKeyMap.has(idx)) {
      const staticDepositKey = this.staticDepositKeyMap.get(idx);
      return staticDepositKey?.publicKey!;
    }

    const staticDepositKey = this.staticDepositKey.deriveChild(
      HARDENED_OFFSET + idx,
    );
    this.staticDepositKeyMap.set(idx, staticDepositKey);
    this.publicKeyToPrivateKeyMap.set(
      bytesToHex(staticDepositKey.publicKey!),
      bytesToHex(staticDepositKey.privateKey!),
    );
    return staticDepositKey.publicKey!;
  }

  async getStaticDepositSigningKey(idx: number): Promise<Uint8Array> {
    if (!this.staticDepositKey) {
      throw new ValidationError("Static deposit key is not set", {
        field: "staticDepositKey",
      });
    }

    if (!this.staticDepositKeyMap.has(idx)) {
      await this.generateStaticDepositKey(idx);
    }

    const staticDepositKey = this.staticDepositKeyMap.get(idx);

    if (!staticDepositKey?.publicKey) {
      throw new ValidationError("Static deposit key is not set", {
        field: "staticDepositKey",
      });
    }

    return staticDepositKey.publicKey;
  }

  async getStaticDepositSecretKey(idx: number): Promise<Uint8Array> {
    if (!this.staticDepositKey) {
      throw new ValidationError("Static deposit key is not set", {
        field: "staticDepositKey",
      });
    }

    if (!this.staticDepositKeyMap.has(idx)) {
      await this.generateStaticDepositKey(idx);
    }

    const staticDepositKey = this.staticDepositKeyMap.get(idx);

    if (!staticDepositKey?.privateKey) {
      throw new ValidationError("Static deposit key is not set", {
        field: "staticDepositKey",
      });
    }

    return staticDepositKey.privateKey;
  }

  async generateMnemonic(): Promise<string> {
    return generateMnemonic(wordlist);
  }

  async mnemonicToSeed(mnemonic: string): Promise<Uint8Array> {
    return await mnemonicToSeed(mnemonic);
  }

  async getTrackedPublicKeys(): Promise<Uint8Array[]> {
    return Array.from(this.publicKeyToPrivateKeyMap.keys()).map(hexToBytes);
  }

  async generatePublicKey(hash?: Uint8Array): Promise<Uint8Array> {
    if (!this.signingKey) {
      throw new ValidationError("Private key is not set", {
        field: "signingKey",
      });
    }

    let newPrivateKey: Uint8Array | null = null;
    if (hash) {
      newPrivateKey = this.deriveSigningKey(hash);
    } else {
      newPrivateKey = secp256k1.utils.randomPrivateKey();
    }

    if (!newPrivateKey) {
      throw new ValidationError("Failed to generate new private key", {
        field: "privateKey",
      });
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
    const SparkFrost = await getSparkFrostModule();
    if (!SparkFrost) {
      throw new ValidationError("SparkFrost module not found", {
        field: "SparkFrost",
      });
    }
    const privateAsPubKeyHex = bytesToHex(privateAsPubKey);
    const signingPrivateKey =
      this.publicKeyToPrivateKeyMap.get(privateAsPubKeyHex);

    if (!signingPrivateKey) {
      throw new ValidationError("Private key not found for public key", {
        field: "privateKey",
      });
    }

    const nonce = this.commitmentToNonceMap.get(selfCommitment);
    if (!nonce) {
      throw new ValidationError("Nonce not found for commitment", {
        field: "nonce",
      });
    }

    const keyPackage: IKeyPackage = {
      secretKey: hexToBytes(signingPrivateKey),
      publicKey: publicKey,
      verifyingKey: verifyingKey,
    };

    return SparkFrost.signFrost({
      message,
      keyPackage,
      nonce,
      selfCommitment,
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
    const SparkFrost = await getSparkFrostModule();
    if (!SparkFrost) {
      throw new ValidationError("SparkFrost module not found", {
        field: "SparkFrost",
      });
    }
    return SparkFrost.aggregateFrost({
      message,
      statechainSignatures,
      statechainPublicKeys,
      verifyingKey,
      statechainCommitments,
      selfCommitment,
      selfPublicKey: publicKey,
      selfSignature,
      adaptorPubKey,
    });
  }

  async createSparkWalletFromSeed(
    seed: Uint8Array | string,
    accountNumber?: number,
  ): Promise<string> {
    if (typeof seed === "string") {
      seed = hexToBytes(seed);
    }

    const { masterPublicKey, identityKey, signingHDKey: signingKey, depositKey, staticDepositHDKey: staticDepositKey } =
      await this.keyGenerator.deriveHDKeysFromSeed(seed, accountNumber ?? 0);

    this.masterPublicKey = masterPublicKey;
    this.identityKey = identityKey;
    this.depositKey = depositKey;
    this.signingKey = signingKey.hdKey;
    this.staticDepositKey = staticDepositKey.hdKey;

    this.publicKeyToPrivateKeyMap.set(
      bytesToHex(identityKey.publicKey),
      bytesToHex(identityKey.privateKey),
    );
    this.publicKeyToPrivateKeyMap.set(
      bytesToHex(depositKey.publicKey),
      bytesToHex(depositKey.privateKey),
    );
    this.publicKeyToPrivateKeyMap.set(
      bytesToHex(staticDepositKey.publicKey),
      bytesToHex(staticDepositKey.privateKey),
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
      throw new ValidationError("Private key not found for public key", {
        field: "privateKey",
        value: bytesToHex(publicKey),
      });
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
      throw new ConfigurationError("Identity key not initialized", {
        configKey: "identityKey",
      });
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
      throw new ConfigurationError("Identity key not initialized", {
        configKey: "identityKey",
      });
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
    if (!this.masterPublicKey) {
      throw new Error("Private key is not set");
    }

    return this.masterPublicKey;
  }

  async validateMessageWithIdentityKey(
    message: Uint8Array,
    signature: Uint8Array,
  ): Promise<boolean> {
    if (!this.identityKey?.publicKey) {
      throw new ConfigurationError("Identity key not initialized", {
        configKey: "identityKey",
      });
    }

    return secp256k1.verify(signature, message, this.identityKey.publicKey);
  }

  async signPsbt(
    psbt: Psbt,
    input: number,
    sighashTypes?: number[],
    receipt?: Receipt,
  ): Promise<Psbt> {
    if (!this.identityKey?.privateKey) {
      throw new ConfigurationError("Identity key not initialized", {
        configKey: "identityKey",
      });
    }
    if (receipt) {
      const receiptPrivateKey = this.getReceiptPrivateKey(receipt);
      const tweakedKeyPair = fromPrivateKey(Buffer.from(receiptPrivateKey));
      psbt.signInput(input, tweakedKeyPair, sighashTypes);
      return psbt;
    }
    const keypair = fromPrivateKey(Buffer.from(this.identityKey!.privateKey));
    psbt.signInput(input, keypair, sighashTypes);
    return psbt;
  }

  private getReceiptPrivateKey(receipt: Receipt): Uint8Array {
    const pxh = Receipt.receiptHash(receipt);
    let innerKey = this.identityKey!!.publicKey!;
    let privateKey = this.identityKey!!.privateKey!;

    if (innerKey[0] === 3) {
      innerKey = Buffer.concat([PARITY, innerKey.slice(1)]);
      privateKey = Buffer.from(privateNegate(privateKey));
    }

    const pxhPubkey = sha256(Buffer.concat([pxh, innerKey]));

    const receiptProof = privateAdd(privateKey, pxhPubkey)!;
    return Buffer.from(receiptProof);
  }

  signTransactionIndex(
    tx: Transaction,
    index: number,
    publicKey: Uint8Array,
  ): void {
    let privateKey: Uint8Array | undefined | null;

    if (
      equalBytes(publicKey, this.identityKey?.publicKey ?? new Uint8Array())
    ) {
      privateKey = this.identityKey?.privateKey;
    } else if (
      equalBytes(publicKey, this.depositKey?.publicKey ?? new Uint8Array())
    ) {
      privateKey = this.depositKey?.privateKey;
    } else {
      privateKey = hexToBytes(
        this.publicKeyToPrivateKeyMap.get(bytesToHex(publicKey)) ?? "",
      );
    }

    if (!privateKey) {
      throw new ValidationError("Private key not found for public key", {
        field: "privateKey",
        value: bytesToHex(publicKey),
      });
    }

    tx.signIdx(privateKey, index);
  }
}
export { DefaultSparkSigner };
export type { SparkSigner };
