import { describe, expect, it } from "@jest/globals";
import { schnorr, secp256k1 } from "@noble/curves/secp256k1";
import { bytesToHex } from "@noble/curves/utils";
import type { Transaction } from "@scure/btc-signer";
import { ConfigOptions } from "../../services/wallet-config.js";
import type { SparkSigner } from "../../signer/signer.js";
import type {
  AggregateFrostParams,
  KeyDerivation,
  SignFrostParams,
  SigningCommitmentWithOptionalNonce,
  SplitSecretWithProofsParams,
} from "../../signer/types.js";
import { NetworkType } from "../../utils/network.js";
import type { VerifiableSecretShare } from "../../utils/secret-sharing.js";
import { walletTypes } from "../test-utils.js";
import { SparkWalletTesting } from "../utils/spark-testing-wallet.js";

describe.each(walletTypes)("wallet", ({ name, Signer }) => {
  it(`${name} - should initialize a wallet`, async () => {
    const seedOrMnemonics = [
      "wear cattle behind affair parade error luxury profit just rate arch cigar",
      "logic ripple layer execute smart disease marine hero monster talent crucial unfair horror shadow maze abuse avoid story loop jaguar sphere trap decrease turn",
      "936eda5945550ab384b4fd91fd6024360f6fdf1ecd9a181fb374d07cdbff0985528dc7aff7305da7dab26ce88425f692d4e3bfefbb27e1770b7773bc3c69e7bb",
      "5904c8ec7a0f8748e4f3d82840cb9736857b8feec921ccd7ceba20d47c9e3e2f3050e6beefefe73a2af8740ff4dc203a33771fe680d9e24934f8a2784eda53be",
    ];
    const networks: NetworkType[] = ["LOCAL"];

    for (const seedOrMnemonic of seedOrMnemonics) {
      for (const network of networks) {
        const options: ConfigOptions = {
          network,
        };
        const { wallet, ...rest } = await SparkWalletTesting.initialize({
          mnemonicOrSeed: seedOrMnemonic,
          options,
          signer: new Signer(),
        });
        expect(wallet).toBeDefined();
      }
    }
  }, 30000);

  it(`${name} - should initialize wallets with different identity keys for different account numbers with the same mnemonic`, async () => {
    const seedOrMnemonics =
      "wear cattle behind affair parade error luxury profit just rate arch cigar";

    const networks: NetworkType[] = ["LOCAL"];

    const { wallet: accountTen } = await SparkWalletTesting.initialize({
      mnemonicOrSeed: seedOrMnemonics,
      accountNumber: 10,
      options: {
        network: "LOCAL",
      },
      signer: new Signer(),
    });
    const { wallet: accountEleven } = await SparkWalletTesting.initialize({
      mnemonicOrSeed: seedOrMnemonics,
      accountNumber: 11,
      options: {
        network: "LOCAL",
      },
      signer: new Signer(),
    });

    const accountTenIdentityKey = await accountTen.getIdentityPublicKey();
    const accountElevenIdentityKey = await accountEleven.getIdentityPublicKey();
    expect(accountTenIdentityKey).not.toEqual(accountElevenIdentityKey);
  });

  it(`${name} - should not initialize a wallet with an invalid seed or mnemonic`, async () => {
    const seedOrMnemonics = [
      "wear cattle behind affair parade error luxury profit just rate arch",
      "jot jot jot jot",
      "936eda5945550ab384b4fd91fd",
      "tb1qzf5a9dwm2gxwkrptsy67xynu4vmr0cvx2zwctg",
    ];

    for (const seedOrMnemonic of seedOrMnemonics) {
      const options: ConfigOptions = {
        network: "LOCAL",
      };
      await expect(
        SparkWalletTesting.initialize({
          mnemonicOrSeed: seedOrMnemonic,
          options,
          signer: new Signer(),
        }),
      ).rejects.toThrow();
    }
  });
});

class PreinitializedTestSigner implements SparkSigner {
  private readonly identityPrivateKey: Uint8Array;
  private readonly depositPrivateKey: Uint8Array;

  constructor(params?: {
    identityPrivateKey?: Uint8Array;
    depositPrivateKey?: Uint8Array;
  }) {
    this.identityPrivateKey =
      params?.identityPrivateKey ?? secp256k1.utils.randomPrivateKey();
    this.depositPrivateKey =
      params?.depositPrivateKey ?? secp256k1.utils.randomPrivateKey();
  }

  async getIdentityPublicKey(): Promise<Uint8Array> {
    return secp256k1.getPublicKey(this.identityPrivateKey);
  }
  async getDepositSigningKey(): Promise<Uint8Array> {
    return secp256k1.getPublicKey(this.depositPrivateKey);
  }
  async getStaticDepositSigningKey(_idx: number): Promise<Uint8Array> {
    // Not used in this test; return a valid pubkey
    return secp256k1.getPublicKey(secp256k1.utils.randomPrivateKey());
  }
  async getStaticDepositSecretKey(_idx: number): Promise<Uint8Array> {
    // Not used in this test
    return secp256k1.utils.randomPrivateKey();
  }

  async generateMnemonic(): Promise<string> {
    throw new Error("Not implemented in PreinitializedTestSigner");
  }
  async mnemonicToSeed(_mnemonic: string): Promise<Uint8Array> {
    throw new Error("Not implemented in PreinitializedTestSigner");
  }
  async createSparkWalletFromSeed(
    _seed: Uint8Array | string,
    _accountNumber?: number,
  ): Promise<string> {
    throw new Error("Not implemented in PreinitializedTestSigner");
  }
  async getPublicKeyFromDerivation(
    _keyDerivation?: KeyDerivation,
  ): Promise<Uint8Array> {
    throw new Error("Not implemented in PreinitializedTestSigner");
  }

  async signSchnorrWithIdentityKey(message: Uint8Array): Promise<Uint8Array> {
    return schnorr.sign(message, this.identityPrivateKey);
  }

  async subtractPrivateKeysGivenDerivationPaths(
    _first: string,
    _second: string,
  ): Promise<Uint8Array> {
    throw new Error("Not implemented in PreinitializedTestSigner");
  }
  async subtractAndSplitSecretWithProofsGivenDerivations(
    _params: Omit<SplitSecretWithProofsParams, "secret"> & {
      first: KeyDerivation;
      second?: KeyDerivation | undefined;
    },
  ): Promise<VerifiableSecretShare[]> {
    throw new Error("Not implemented in PreinitializedTestSigner");
  }
  async subtractSplitAndEncrypt(
    _params: Omit<SplitSecretWithProofsParams, "secret"> & {
      first: KeyDerivation;
      second: KeyDerivation;
      receiverPublicKey: Uint8Array;
    },
  ): Promise<{ shares: VerifiableSecretShare[]; secretCipher: Uint8Array }> {
    throw new Error("Not implemented in PreinitializedTestSigner");
  }
  async splitSecretWithProofs(
    _params: SplitSecretWithProofsParams,
  ): Promise<VerifiableSecretShare[]> {
    throw new Error("Not implemented in PreinitializedTestSigner");
  }
  async signFrost(_params: SignFrostParams): Promise<Uint8Array> {
    throw new Error("Not implemented in PreinitializedTestSigner");
  }
  async aggregateFrost(_params: AggregateFrostParams): Promise<Uint8Array> {
    throw new Error("Not implemented in PreinitializedTestSigner");
  }

  async signMessageWithIdentityKey(
    message: Uint8Array,
    compact?: boolean,
  ): Promise<Uint8Array> {
    const signature = secp256k1.sign(message, this.identityPrivateKey);
    return compact ? signature.toCompactRawBytes() : signature.toDERRawBytes();
  }
  async validateMessageWithIdentityKey(
    message: Uint8Array,
    signature: Uint8Array,
  ): Promise<boolean> {
    return secp256k1.verify(
      signature,
      message,
      secp256k1.getPublicKey(this.identityPrivateKey),
    );
  }

  signTransactionIndex(
    _tx: Transaction,
    _index: number,
    _publicKey: Uint8Array,
  ): void {
    // Not used in this test
    return;
  }

  async decryptEcies(_ciphertext: Uint8Array): Promise<Uint8Array> {
    throw new Error("Not implemented in PreinitializedTestSigner");
  }

  async getRandomSigningCommitment(): Promise<SigningCommitmentWithOptionalNonce> {
    // Provide a structurally valid fake commitment
    const binding = secp256k1.utils.randomPrivateKey();
    const hiding = secp256k1.utils.randomPrivateKey();
    return { commitment: { binding, hiding } };
  }
}

it("PreinitializedTestSigner - should initialize a wallet without seed using pre-existing keys", async () => {
  const identityPrivateKey = secp256k1.utils.randomPrivateKey();
  const signer = new PreinitializedTestSigner({ identityPrivateKey });

  const { wallet } = await SparkWalletTesting.initialize({
    options: {
      network: "LOCAL",
      signerWithPreExistingKeys: true,
    },
    signer,
  });

  expect(wallet).toBeDefined();
  const identityPubkeyHex = bytesToHex(
    secp256k1.getPublicKey(identityPrivateKey),
  );
  const walletIdentityPubkey = await wallet.getIdentityPublicKey();
  expect(walletIdentityPubkey).toEqual(identityPubkeyHex);

  const sparkAddress = await wallet.getSparkAddress();
  expect(sparkAddress).toBeDefined();
});
