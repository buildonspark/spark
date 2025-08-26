import { privateNegate } from "@bitcoinerlab/secp256k1";
import { schnorr, secp256k1 } from "@noble/curves/secp256k1";
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
import { taprootTweakPrivKey } from "@scure/btc-signer/utils";
import * as ecies from "eciesjs";
import { isReactNative } from "../constants.js";
import { ConfigurationError, ValidationError } from "../errors/types.js";
import { IKeyPackage } from "../spark_bindings/types.js";
import { subtractPrivateKeys } from "../utils/keys.js";
import {
  splitSecretWithProofs,
  VerifiableSecretShare,
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
} from "./types.js";

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

interface SparkKeysGenerator {
  deriveKeysFromSeed(
    seed: Uint8Array,
    accountNumber: number,
  ): Promise<{
    identityKey: KeyPair;
    signingHDKey: DerivedHDKey;
    depositKey: KeyPair;
    staticDepositHDKey: DerivedHDKey;
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

    if (
      !identityKey.privateKey ||
      !identityKey.publicKey ||
      !signingKey.privateKey ||
      !signingKey.publicKey ||
      !depositKey.privateKey ||
      !depositKey.publicKey ||
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

class TaprootOutputKeysGenerator implements SparkKeysGenerator {
  constructor(private readonly useAddressIndex: boolean = false) {}

  async deriveKeysFromSeed(
    seed: Uint8Array,
    accountNumber: number,
  ): Promise<{
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

    const derivationPath = this.useAddressIndex
      ? `m/86'/0'/0'/0/${accountNumber}`
      : `m/86'/0'/${accountNumber}'/0/0`;

    const taprootInternalKey = hdkey.derive(derivationPath);

    let tweakedPrivateKey = taprootTweakPrivKey(taprootInternalKey.privateKey!);
    let tweakedPublicKey = secp256k1.getPublicKey(tweakedPrivateKey);

    // always use the even key
    if (tweakedPublicKey[0] === 3) {
      tweakedPrivateKey = privateNegate(tweakedPrivateKey);
      tweakedPublicKey = secp256k1.getPublicKey(tweakedPrivateKey);
    }

    const identityKey = {
      publicKey: tweakedPublicKey,
      privateKey: tweakedPrivateKey,
    };

    const signingKey = hdkey.derive(`${derivationPath}/1'`);
    const depositKey = hdkey.derive(`${derivationPath}/2'`);
    const staticDepositKey = hdkey.derive(`${derivationPath}/3'`);

    if (
      !signingKey.privateKey ||
      !signingKey.publicKey ||
      !depositKey.privateKey ||
      !depositKey.publicKey ||
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

interface SparkSigner {
  getIdentityPublicKey(): Promise<Uint8Array>;
  getDepositSigningKey(): Promise<Uint8Array>;
  getStaticDepositSigningKey(idx: number): Promise<Uint8Array>;
  getStaticDepositSecretKey(idx: number): Promise<Uint8Array>;

  generateMnemonic(): Promise<string>;
  mnemonicToSeed(mnemonic: string): Promise<Uint8Array>;

  createSparkWalletFromSeed(
    seed: Uint8Array | string,
    accountNumber?: number,
  ): Promise<string>;

  getPublicKeyFromDerivation(
    keyDerivation?: KeyDerivation,
  ): Promise<Uint8Array>;

  signSchnorrWithIdentityKey(message: Uint8Array): Promise<Uint8Array>;

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
    params: Omit<SplitSecretWithProofsParams, "secret"> & {
      first: KeyDerivation;
      second: KeyDerivation;
      receiverPublicKey: Uint8Array;
    },
  ): Promise<{
    shares: VerifiableSecretShare[];
    secretCipher: Uint8Array;
  }>;

  splitSecretWithProofs(
    params: SplitSecretWithProofsParams,
  ): Promise<VerifiableSecretShare[]>;

  signFrost(params: SignFrostParams): Promise<Uint8Array>;
  aggregateFrost(params: AggregateFrostParams): Promise<Uint8Array>;

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

  decryptEcies(ciphertext: Uint8Array): Promise<Uint8Array>;

  getRandomSigningCommitment(): Promise<SigningCommitmentWithOptionalNonce>;

  getDepositSigningKey(): Promise<Uint8Array>;
}

class DefaultSparkSigner implements SparkSigner {
  private identityKey: KeyPair | null = null;
  private signingKey: HDKey | null = null;
  private depositKey: KeyPair | null = null;
  private staticDepositKey: HDKey | null = null;

  protected commitmentToNonceMap: Map<SigningCommitment, SigningNonce> =
    new Map();

  private readonly keysGenerator: SparkKeysGenerator;

  constructor({
    sparkKeysGenerator,
  }: { sparkKeysGenerator?: SparkKeysGenerator } = {}) {
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
    const receiverEciesPrivKey = ecies.PrivateKey.fromHex(
      bytesToHex(this.identityKey.privateKey),
    );
    const privateKey = ecies.decrypt(receiverEciesPrivKey.toHex(), ciphertext);

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
  }): Promise<{
    shares: VerifiableSecretShare[];
    secretCipher: Uint8Array;
  }> {
    const firstPrivateKey =
      await this.getSigningPrivateKeyFromDerivation(first);
    const secondPrivateKey =
      await this.getSigningPrivateKeyFromDerivation(second);

    const resultPrivKey = subtractPrivateKeys(
      firstPrivateKey,
      secondPrivateKey,
    );

    return {
      shares: await this.splitSecretWithProofs({
        secret: resultPrivKey,
        curveOrder,
        threshold,
        numShares,
      }),
      secretCipher: ecies.encrypt(receiverPublicKey, secondPrivateKey),
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

  async signFrost({
    message,
    keyDerivation,
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

    const signingPrivateKey =
      await this.getSigningPrivateKeyFromDerivation(keyDerivation);

    if (!signingPrivateKey) {
      throw new ValidationError("Private key not found for public key", {
        field: "privateKey",
      });
    }

    const commitment = selfCommitment.commitment;
    const nonce = this.commitmentToNonceMap.get(commitment);
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

    const logMessage = bytesToHex(message);

    const result = await SparkFrost.signFrost({
      message,
      keyPackage,
      nonce,
      selfCommitment: commitment,
      statechainCommitments,
      adaptorPubKey,
    });

    return result;
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
      selfCommitment: selfCommitment.commitment,
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

    const {
      identityKey,
      signingHDKey: signingKey,
      depositKey,
      staticDepositHDKey: staticDepositKey,
    } = await this.keysGenerator.deriveKeysFromSeed(seed, accountNumber ?? 0);

    this.identityKey = identityKey;
    this.depositKey = depositKey;
    this.signingKey = signingKey.hdKey;
    this.staticDepositKey = staticDepositKey.hdKey;

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
    const receiverEciesPrivKey = ecies.PrivateKey.fromHex(
      bytesToHex(this.identityKey.privateKey),
    );
    const privateKey = ecies.decrypt(receiverEciesPrivKey.toHex(), ciphertext);
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
}

/**
 * StatelessSparkSigner is different from DefaultSparkSigner in that it does not store
 * nonces in internal state. StatelessSparkSigner should only be used in a secure environment.
 *
 * @extends DefaultSparkSigner
 */
class UnsafeStatelessSparkSigner extends DefaultSparkSigner {
  constructor({
    sparkKeysGenerator,
  }: { sparkKeysGenerator?: SparkKeysGenerator } = {}) {
    super({
      sparkKeysGenerator,
    });
  }

  async getRandomSigningCommitment(): Promise<SigningCommitmentWithOptionalNonce> {
    const nonce = getRandomSigningNonce();
    const commitment = getSigningCommitmentFromNonce(nonce);
    return {
      commitment,
      nonce,
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
    const SparkFrost = await getSparkFrostModule();
    if (!SparkFrost) {
      throw new ValidationError("SparkFrost module not found", {
        field: "SparkFrost",
      });
    }

    const signingPrivateKey =
      await this.getSigningPrivateKeyFromDerivation(keyDerivation);

    if (!signingPrivateKey) {
      throw new ValidationError("Private key not found for public key", {
        field: "privateKey",
      });
    }

    const { commitment, nonce } = selfCommitment;
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

    return SparkFrost.signFrost({
      message,
      keyPackage,
      nonce,
      selfCommitment: commitment,
      statechainCommitments,
      adaptorPubKey,
    });
  }
}

class TaprootSparkSigner extends DefaultSparkSigner {
  constructor(useAddressIndex = false) {
    super({
      sparkKeysGenerator: new TaprootOutputKeysGenerator(useAddressIndex),
    });
  }
}

class NativeSegwitSparkSigner extends DefaultSparkSigner {
  constructor(useAddressIndex = false) {
    super({
      sparkKeysGenerator: new DerivationPathKeysGenerator(
        useAddressIndex ? "m/84'/0'/0'/0/?" : "m/84'/0'/?'/0/0",
      ),
    });
  }
}

class WrappedSegwitSparkSigner extends DefaultSparkSigner {
  constructor(useAddressIndex = false) {
    super({
      sparkKeysGenerator: new DerivationPathKeysGenerator(
        useAddressIndex ? "m/49'/0'/0'/0/?" : "m/49'/0'/?'/0/0",
      ),
    });
  }
}

class LegacyBitcoinSparkSigner extends DefaultSparkSigner {
  constructor(useAddressIndex = false) {
    super({
      sparkKeysGenerator: new DerivationPathKeysGenerator(
        useAddressIndex ? "m/44'/0'/0'/0/?" : "m/44'/0'/?'/0/0",
      ),
    });
  }
}

export {
  DefaultSparkSigner,
  LegacyBitcoinSparkSigner,
  NativeSegwitSparkSigner,
  TaprootOutputKeysGenerator,
  TaprootSparkSigner,
  UnsafeStatelessSparkSigner,
  WrappedSegwitSparkSigner,
  type SparkSigner,
};
