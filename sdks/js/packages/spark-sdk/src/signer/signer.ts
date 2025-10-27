import { schnorr, secp256k1 } from "@noble/curves/secp256k1";
import { hmac } from "@noble/hashes/hmac";
import {
  bytesToHex,
  bytesToNumberBE,
  equalBytes,
  hexToBytes,
} from "@noble/curves/utils";
import { sha256 } from "@noble/hashes/sha2";
import { HDKey } from "@scure/bip32";
import { generateMnemonic, mnemonicToSeed } from "@scure/bip39";
import { wordlist } from "@scure/bip39/wordlists/english";
import { Transaction } from "@scure/btc-signer";
import { ConfigurationError, ValidationError } from "../errors/types.js";
import { getSparkFrost } from "../spark-bindings/spark-bindings.js";
import {
  AggregateFrostBindingParams,
  IKeyPackage,
  SignFrostBindingParams,
} from "../spark-bindings/types.js";
import { subtractPrivateKeys } from "../utils/keys.js";
import {
  splitSecretWithProofs,
  type VerifiableSecretShare,
} from "../utils/secret-sharing.js";
import {
  getRandomSigningNonce,
  getSigningCommitmentFromNonce,
} from "../utils/signing.js";
import {
  KeyDerivationType,
  SigningCommitmentWithOptionalNonce,
  type AggregateFrostParams,
  type DerivedHDKey,
  type KeyDerivation,
  type KeyPair,
  type SignFrostParams,
  type SigningCommitment,
  type SigningNonce,
  type SplitSecretWithProofsParams,
  type SubtractSplitAndEncryptParams,
  type SubtractSplitAndEncryptResult,
} from "./types.js";

interface SparkKeysGenerator {
  deriveKeysFromSeed(
    seed: Uint8Array,
    accountNumber: number,
  ): Promise<{
    identityKey: KeyPair;
    signingHDKey: DerivedHDKey;
    depositKey: KeyPair;
    staticDepositHDKey: DerivedHDKey;
    HTLCPreimageHDKey: DerivedHDKey;
  }>;
}

const HARDENED_OFFSET = 0x80000000; // 2^31

class DefaultSparkKeysGenerator implements SparkKeysGenerator {
  async deriveKeysFromSeed(
    seed: Uint8Array,
    accountNumber: number,
  ): Promise<{
    identityKey: KeyPair;
    signingHDKey: DerivedHDKey;
    depositKey: KeyPair;
    staticDepositHDKey: DerivedHDKey;
    HTLCPreimageHDKey: DerivedHDKey;
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
    const htlcPreimageKey = hdkey.derive(`m/8797555'/${accountNumber}'/4'`);
    if (
      !identityKey.privateKey ||
      !depositKey.privateKey ||
      !signingKey.privateKey ||
      !identityKey.publicKey ||
      !depositKey.publicKey ||
      !signingKey.publicKey ||
      !staticDepositKey.privateKey ||
      !staticDepositKey.publicKey ||
      !htlcPreimageKey.privateKey ||
      !htlcPreimageKey.publicKey
    ) {
      throw new ValidationError(
        "Failed to derive all required keys from seed",
        {
          field: "derivedKeys",
        },
      );
    }

    return {
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
      HTLCPreimageHDKey: {
        hdKey: htlcPreimageKey,
        privateKey: htlcPreimageKey.privateKey,
        publicKey: htlcPreimageKey.publicKey,
      },
    };
  }
}

class DerivationPathKeysGenerator implements SparkKeysGenerator {
  constructor(private readonly derivationPathTemplate: string) {}

  async deriveKeysFromSeed(
    seed: Uint8Array,
    accountNumber: number,
  ): Promise<{
    identityKey: KeyPair;
    signingHDKey: DerivedHDKey;
    depositKey: KeyPair;
    staticDepositHDKey: DerivedHDKey;
    HTLCPreimageHDKey: DerivedHDKey;
  }> {
    const hdkey = HDKey.fromMasterSeed(seed);

    if (!hdkey.privateKey || !hdkey.publicKey) {
      throw new ValidationError("Failed to derive keys from seed", {
        field: "hdkey",
        value: seed,
      });
    }

    const derivationPath = this.derivationPathTemplate.replaceAll(
      "?",
      accountNumber.toString(),
    );

    const identityKey = hdkey.derive(derivationPath);
    const signingKey = hdkey.derive(`${derivationPath}/1'`);
    const depositKey = hdkey.derive(`${derivationPath}/2'`);
    const staticDepositKey = hdkey.derive(`${derivationPath}/3'`);
    const htlcPreimageKey = hdkey.derive(`${derivationPath}/4'`);
    if (
      !identityKey.privateKey ||
      !identityKey.publicKey ||
      !signingKey.privateKey ||
      !signingKey.publicKey ||
      !depositKey.privateKey ||
      !depositKey.publicKey ||
      !staticDepositKey.privateKey ||
      !staticDepositKey.publicKey ||
      !htlcPreimageKey.privateKey ||
      !htlcPreimageKey.publicKey
    ) {
      throw new ValidationError(
        "Failed to derive all required keys from seed",
        {
          field: "derivedKeys",
        },
      );
    }

    return {
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
      HTLCPreimageHDKey: {
        hdKey: htlcPreimageKey,
        privateKey: htlcPreimageKey.privateKey,
        publicKey: htlcPreimageKey.publicKey,
      },
    };
  }
}

interface SparkSigner {
  getIdentityPublicKey(): Promise<Uint8Array>;
  getDepositSigningKey(): Promise<Uint8Array>;
  getStaticDepositSigningKey(idx: number): Promise<Uint8Array>;
  getStaticDepositSecretKey(idx: number): Promise<Uint8Array>;

  generateMnemonic(): Promise<string>;
  mnemonicToSeed(mnemonic: string): Promise<Uint8Array>;
  signSchnorrWithIdentityKey(message: Uint8Array): Promise<Uint8Array>;
  signFrost(params: SignFrostParams): Promise<Uint8Array>;
  aggregateFrost(params: AggregateFrostParams): Promise<Uint8Array>;
  decryptEcies(ciphertext: Uint8Array): Promise<Uint8Array>;
  getRandomSigningCommitment(): Promise<SigningCommitmentWithOptionalNonce>;
  getDepositSigningKey(): Promise<Uint8Array>;

  getNonceForSelfCommitment(
    selfCommitment: SigningCommitmentWithOptionalNonce,
  ): SigningNonce | undefined;

  createSparkWalletFromSeed(
    seed: Uint8Array | string,
    accountNumber?: number,
  ): Promise<string>;

  getPublicKeyFromDerivation(
    keyDerivation?: KeyDerivation,
  ): Promise<Uint8Array>;

  subtractPrivateKeysGivenDerivationPaths(
    first: string,
    second: string,
  ): Promise<Uint8Array>;

  subtractAndSplitSecretWithProofsGivenDerivations(
    params: Omit<SplitSecretWithProofsParams, "secret"> & {
      first: KeyDerivation;
      second?: KeyDerivation | undefined;
    },
  ): Promise<VerifiableSecretShare[]>;

  subtractSplitAndEncrypt(
    params: SubtractSplitAndEncryptParams,
  ): Promise<SubtractSplitAndEncryptResult>;

  splitSecretWithProofs(
    params: SplitSecretWithProofsParams,
  ): Promise<VerifiableSecretShare[]>;

  signMessageWithIdentityKey(
    message: Uint8Array,
    /* If compact is true, the signature should be in
       ecdsa compact format else it should be in DER format */
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

  htlcHMAC(transferID: string): Promise<Uint8Array>;
}

type SparkSignerConstructorParams = {
  sparkKeysGenerator?: SparkKeysGenerator;
};

class DefaultSparkSigner implements SparkSigner {
  private identityKey: KeyPair | null = null;
  private signingKey: HDKey | null = null;
  private depositKey: KeyPair | null = null;
  private staticDepositKey: HDKey | null = null;
  private htlcPreimageKey: HDKey | null = null;
  private readonly keysGenerator: SparkKeysGenerator;

  protected commitmentToNonceMap: Map<SigningCommitment, SigningNonce> =
    new Map();

  constructor({ sparkKeysGenerator }: SparkSignerConstructorParams = {}) {
    this.keysGenerator = sparkKeysGenerator ?? new DefaultSparkKeysGenerator();
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

  private async decryptEciesToPrivateKey(
    ciphertext: Uint8Array,
  ): Promise<Uint8Array> {
    if (!this.identityKey?.privateKey) {
      throw new ConfigurationError("Identity key not initialized", {
        configKey: "identityKey",
      });
    }
    const sparkFrost = getSparkFrost();
    const privateKey = await sparkFrost.decryptEcies(
      ciphertext,
      this.identityKey.privateKey,
    );

    return privateKey;
  }

  protected async getSigningPrivateKeyFromDerivation(
    keyDerivation: KeyDerivation,
  ): Promise<Uint8Array> {
    switch (keyDerivation.type) {
      case KeyDerivationType.LEAF:
        return this.deriveSigningKey(sha256(keyDerivation.path));
      case KeyDerivationType.DEPOSIT:
        return this.depositKey?.privateKey ?? new Uint8Array();
      case KeyDerivationType.STATIC_DEPOSIT:
        return this.getStaticDepositSecretKey(keyDerivation.path);
      case KeyDerivationType.ECIES:
        return this.decryptEciesToPrivateKey(keyDerivation.path);
      case KeyDerivationType.RANDOM:
        return secp256k1.utils.randomPrivateKey();
    }
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

  async getStaticDepositSigningKey(idx: number): Promise<Uint8Array> {
    const staticDepositKey = await this.getStaticDepositSecretKey(idx);
    return secp256k1.getPublicKey(staticDepositKey);
  }

  async getStaticDepositSecretKey(idx: number): Promise<Uint8Array> {
    if (!this.staticDepositKey) {
      throw new ValidationError("Static deposit key is not set", {
        field: "staticDepositKey",
      });
    }

    const staticDepositKey = this.staticDepositKey.deriveChild(
      HARDENED_OFFSET + idx,
    );

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

  async getPublicKeyFromDerivation(
    keyDerivation: KeyDerivation,
  ): Promise<Uint8Array> {
    const privateKey =
      await this.getSigningPrivateKeyFromDerivation(keyDerivation);
    return secp256k1.getPublicKey(privateKey);
  }

  async subtractPrivateKeysGivenDerivationPaths(
    first: string,
    second: string,
  ): Promise<Uint8Array> {
    const firstPrivateKey = this.deriveSigningKey(sha256(first));
    const secondPrivateKey = this.deriveSigningKey(sha256(second));

    const resultPrivKey = subtractPrivateKeys(
      firstPrivateKey,
      secondPrivateKey,
    );
    const resultPubKey = secp256k1.getPublicKey(resultPrivKey);

    return resultPubKey;
  }

  async subtractAndSplitSecretWithProofsGivenDerivations({
    first,
    second,
    curveOrder,
    threshold,
    numShares,
  }: Omit<SplitSecretWithProofsParams, "secret"> & {
    first: KeyDerivation;
    second: KeyDerivation;
  }): Promise<VerifiableSecretShare[]> {
    const firstPrivateKey =
      await this.getSigningPrivateKeyFromDerivation(first);
    const secondPrivateKey =
      await this.getSigningPrivateKeyFromDerivation(second);

    const resultPrivKey = subtractPrivateKeys(
      firstPrivateKey,
      secondPrivateKey,
    );

    return await this.splitSecretWithProofs({
      secret: resultPrivKey,
      curveOrder,
      threshold,
      numShares,
    });
  }

  async subtractSplitAndEncrypt({
    first,
    second,
    curveOrder,
    threshold,
    numShares,
    receiverPublicKey,
  }: Omit<SplitSecretWithProofsParams, "secret"> & {
    first: KeyDerivation;
    second: KeyDerivation;
    receiverPublicKey: Uint8Array;
  }): Promise<SubtractSplitAndEncryptResult> {
    const firstPrivateKey =
      await this.getSigningPrivateKeyFromDerivation(first);
    const secondPrivateKey =
      await this.getSigningPrivateKeyFromDerivation(second);

    const resultPrivKey = subtractPrivateKeys(
      firstPrivateKey,
      secondPrivateKey,
    );

    const sparkFrost = getSparkFrost();
    return {
      shares: await this.splitSecretWithProofs({
        secret: resultPrivKey,
        curveOrder,
        threshold,
        numShares,
      }),
      secretCipher: await sparkFrost.encryptEcies(
        secondPrivateKey,
        receiverPublicKey,
      ),
    };
  }

  async splitSecretWithProofs({
    secret,
    curveOrder,
    threshold,
    numShares,
  }: SplitSecretWithProofsParams): Promise<VerifiableSecretShare[]> {
    const secretAsInt = bytesToNumberBE(secret);
    return splitSecretWithProofs(secretAsInt, curveOrder, threshold, numShares);
  }

  getNonceForSelfCommitment(
    selfCommitment: SigningCommitmentWithOptionalNonce,
  ) {
    const nonce = this.commitmentToNonceMap.get(selfCommitment.commitment);
    return nonce;
  }

  async buildSignFrostParams({
    message,
    keyDerivation,
    publicKey,
    verifyingKey,
    selfCommitment,
    statechainCommitments,
    adaptorPubKey,
  }: SignFrostParams): Promise<SignFrostBindingParams> {
    const signingPrivateKey =
      await this.getSigningPrivateKeyFromDerivation(keyDerivation);

    if (!signingPrivateKey) {
      throw new ValidationError("Private key not found for public key", {
        field: "privateKey",
      });
    }

    const nonce = this.getNonceForSelfCommitment(selfCommitment);
    if (!nonce) {
      throw new ValidationError("Nonce not found for commitment", {
        field: "nonce",
      });
    }

    const keyPackage: IKeyPackage = {
      secretKey: signingPrivateKey,
      publicKey: publicKey,
      verifyingKey: verifyingKey,
    };

    return {
      message,
      keyPackage,
      nonce,
      selfCommitment: selfCommitment.commitment,
      statechainCommitments,
      adaptorPubKey,
    };
  }

  async buildAggregateFrostParams({
    message,
    publicKey,
    verifyingKey,
    selfCommitment,
    statechainCommitments,
    adaptorPubKey,
    selfSignature,
    statechainSignatures,
    statechainPublicKeys,
  }: AggregateFrostParams): Promise<AggregateFrostBindingParams> {
    return {
      message,
      statechainSignatures,
      statechainPublicKeys,
      verifyingKey,
      statechainCommitments,
      selfCommitment: selfCommitment.commitment,
      selfPublicKey: publicKey,
      selfSignature,
      adaptorPubKey,
    };
  }

  async signFrost({
    message,
    keyDerivation,
    publicKey,
    verifyingKey,
    selfCommitment,
    statechainCommitments,
    adaptorPubKey,
  }: SignFrostParams): Promise<Uint8Array> {
    const signFrostParams = await this.buildSignFrostParams({
      message,
      keyDerivation,
      publicKey,
      verifyingKey,
      selfCommitment,
      statechainCommitments,
      adaptorPubKey,
    });
    const sparkFrost = getSparkFrost();
    return sparkFrost.signFrost(signFrostParams);
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
    const aggregateFrostParams = await this.buildAggregateFrostParams({
      message,
      publicKey,
      verifyingKey,
      selfCommitment,
      statechainCommitments,
      adaptorPubKey,
      selfSignature,
      statechainSignatures,
      statechainPublicKeys,
    });
    const sparkFrost = getSparkFrost();
    return sparkFrost.aggregateFrost(aggregateFrostParams);
  }

  async createSparkWalletFromSeed(
    seed: Uint8Array | string,
    accountNumber?: number,
  ): Promise<string> {
    if (typeof seed === "string") {
      seed = hexToBytes(seed);
    }

    const {
      identityKey,
      signingHDKey: signingKey,
      depositKey,
      staticDepositHDKey: staticDepositKey,
      HTLCPreimageHDKey: htlcPreimageKey,
    } = await this.keysGenerator.deriveKeysFromSeed(seed, accountNumber ?? 0);

    this.identityKey = identityKey;
    this.depositKey = depositKey;
    this.signingKey = signingKey.hdKey;
    this.staticDepositKey = staticDepositKey.hdKey;
    this.htlcPreimageKey = htlcPreimageKey.hdKey;

    return bytesToHex(identityKey.publicKey);
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

  async decryptEcies(ciphertext: Uint8Array): Promise<Uint8Array> {
    if (!this.identityKey?.privateKey) {
      throw new ConfigurationError("Identity key not initialized", {
        configKey: "identityKey",
      });
    }
    const sparkFrost = getSparkFrost();
    const privateKey = await sparkFrost.decryptEcies(
      ciphertext,
      this.identityKey.privateKey,
    );
    const publicKey = secp256k1.getPublicKey(privateKey);

    return publicKey;
  }

  async getRandomSigningCommitment(): Promise<SigningCommitmentWithOptionalNonce> {
    const nonce = getRandomSigningNonce();
    const commitment = getSigningCommitmentFromNonce(nonce);
    this.commitmentToNonceMap.set(commitment, nonce);
    return {
      commitment,
    };
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
    }

    if (!privateKey) {
      throw new ValidationError("Private key not found for public key", {
        field: "privateKey",
        value: bytesToHex(publicKey),
      });
    }

    tx.signIdx(privateKey, index);
  }

  async htlcHMAC(transferID: string): Promise<Uint8Array> {
    if (!this.htlcPreimageKey?.privateKey) {
      throw new ConfigurationError("HTLC preimage key not initialized", {
        configKey: "htlcPreimageKey",
      });
    }
    return hmac(sha256, this.htlcPreimageKey.privateKey, transferID);
  }
}

/**
 * StatelessSparkSigner is different from DefaultSparkSigner in that it does not store
 * nonces in internal state. StatelessSparkSigner should only be used in a secure environment.
 *
 * @extends DefaultSparkSigner
 */
class UnsafeStatelessSparkSigner extends DefaultSparkSigner {
  getNonceForSelfCommitment(
    selfCommitment: SigningCommitmentWithOptionalNonce,
  ): SigningNonce | undefined {
    return selfCommitment.nonce;
  }

  async getRandomSigningCommitment(): Promise<SigningCommitmentWithOptionalNonce> {
    const nonce = getRandomSigningNonce();
    const commitment = getSigningCommitmentFromNonce(nonce);
    return {
      commitment,
      nonce,
    };
  }
}

export { DefaultSparkSigner, UnsafeStatelessSparkSigner, type SparkSigner };
