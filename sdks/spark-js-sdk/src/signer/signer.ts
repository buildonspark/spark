import { bytesToHex, hexToBytes } from "@noble/curves/abstract/utils";
import { secp256k1 } from "@noble/curves/secp256k1";
import { HDKey } from "@scure/bip32";
import * as bip39 from "@scure/bip39";
import { wordlist } from "@scure/bip39/wordlists/english";
import assert from "assert";
import * as ecies from "eciesjs";
import { getRandomSigningNonce } from "../utils/signing";

type SigningNonce = {
  binding: Uint8Array;
  hiding: Uint8Array;
};

type SigningCommitment = {
  binding: Uint8Array;
  hiding: Uint8Array;
};

interface SparkSigner {
  getIdentityPublicKey(): Uint8Array;

  generateMnemonic(): string;
  createSparkWalletFromMnemonic(mnemonic: string): string;
  createSparkWalletFromSeed(seed: Uint8Array | string): string;

  signEcdsaWithIdentityPrivateKey(message: Uint8Array): Uint8Array;
  decryptEcies(ciphertext: Uint8Array): Uint8Array;

  getRandomSigningNonce(): SigningNonce;
  getSigningCommitmentFromNonce(nonce: SigningNonce): SigningCommitment;
}

class DefaultSparkSigner implements SparkSigner {
  private identityPrivateKey: Uint8Array | null = null;

  getIdentityPublicKey(): Uint8Array {
    if (!this.identityPrivateKey) {
      throw new Error("Private key is not set");
    }

    return secp256k1.getPublicKey(this.identityPrivateKey);
  }

  generateMnemonic(): string {
    return bip39.generateMnemonic(wordlist);
  }

  createSparkWalletFromMnemonic(mnemonic: string): string {
    const seed = bip39.mnemonicToSeedSync(mnemonic);

    return this.createSparkWalletFromSeed(seed);
  }

  createSparkWalletFromSeed(seed: Uint8Array): string {
    if (typeof seed === "string") {
      seed = hexToBytes(seed);
    }

    const hdkey = HDKey.fromMasterSeed(seed);

    assert(hdkey.privateKey, "Private key is not set");

    this.identityPrivateKey = hdkey.privateKey;

    return bytesToHex(secp256k1.getPublicKey(hdkey.privateKey, true));
  }

  signEcdsaWithIdentityPrivateKey(message: Uint8Array): Uint8Array {
    if (!this.identityPrivateKey) {
      throw new Error("Private key is not set");
    }

    return secp256k1.sign(message, this.identityPrivateKey).toCompactRawBytes();
  }

  decryptEcies(ciphertext: Uint8Array): Uint8Array {
    if (!this.identityPrivateKey) {
      throw new Error("Private key is not set");
    }

    const receiverEciesPrivKey = ecies.PrivateKey.fromHex(
      bytesToHex(this.identityPrivateKey)
    );

    return ecies.decrypt(receiverEciesPrivKey.toHex(), ciphertext);
  }

  getRandomSigningNonce(): SigningNonce {
    return getRandomSigningNonce();
  }

  getSigningCommitmentFromNonce(nonce: SigningNonce): SigningCommitment {
    throw new Error("Not implemented");
  }
}

export { DefaultSparkSigner, SparkSigner };
