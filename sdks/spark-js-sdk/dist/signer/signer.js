import { bytesToHex, bytesToNumberBE, hexToBytes, } from "@noble/curves/abstract/utils";
import { schnorr, secp256k1 } from "@noble/curves/secp256k1";
import { HDKey } from "@scure/bip32";
import * as bip39 from "@scure/bip39";
import { wordlist } from "@scure/bip39/wordlists/english";
import { sha256 } from "@scure/btc-signer/utils";
import { Buffer } from "buffer";
import * as ecies from "eciesjs";
import { generateAdaptorFromSignature } from "../utils/adaptor-signature.js";
import { subtractPrivateKeys } from "../utils/keys.js";
import { splitSecretWithProofs, } from "../utils/secret-sharing.js";
import { createWasmSigningCommitment, createWasmSigningNonce, getRandomSigningNonce, getSigningCommitmentFromNonce, } from "../utils/signing.js";
import { aggregateFrost, signFrost } from "../utils/wasm.js";
import { KeyPackage } from "../wasm/spark_bindings.js";
globalThis.Buffer = Buffer;
class DefaultSparkSigner {
    identityPrivateKey = null;
    // <hex, hex>
    publicKeyToPrivateKeyMap = new Map();
    commitmentToNonceMap = new Map();
    deriveSigningKey(hash) {
        if (!this.identityPrivateKey) {
            throw new Error("Private key is not set");
        }
        let amount = 0;
        for (let i = 0; i < 8; i++) {
            const view = new DataView(hash.buffer, i * 4, 4);
            amount += view.getUint32(0, false);
            amount = amount % 0x80000000;
        }
        const newPrivateKey = this.identityPrivateKey.deriveChild(Number(amount) + 0x80000000).privateKey;
        if (!newPrivateKey) {
            throw new Error("Failed to recover signing key");
        }
        return newPrivateKey;
    }
    async restoreSigningKeysFromLeafs(leafs) {
        if (!this.identityPrivateKey) {
            throw new Error("Private key is not set");
        }
        for (const leaf of leafs) {
            const hash = sha256(leaf.id);
            const privateKey = this.deriveSigningKey(hash);
            const publicKey = secp256k1.getPublicKey(privateKey);
            this.publicKeyToPrivateKeyMap.set(bytesToHex(publicKey), bytesToHex(privateKey));
        }
    }
    async getSchnorrPublicKey(publicKey) {
        const privateKey = this.publicKeyToPrivateKeyMap.get(bytesToHex(publicKey));
        if (!privateKey) {
            throw new Error("Private key is not set");
        }
        return schnorr.getPublicKey(hexToBytes(privateKey));
    }
    async signSchnorr(message, publicKey) {
        const privateKey = this.publicKeyToPrivateKeyMap.get(bytesToHex(publicKey));
        if (!privateKey) {
            throw new Error("Private key is not set");
        }
        return schnorr.sign(message, hexToBytes(privateKey));
    }
    async getIdentityPublicKey() {
        if (!this.identityPrivateKey?.privateKey) {
            throw new Error("Private key is not set");
        }
        return secp256k1.getPublicKey(this.identityPrivateKey.privateKey);
    }
    async generateMnemonic() {
        return bip39.generateMnemonic(wordlist);
    }
    async createSparkWalletFromMnemonic(mnemonic) {
        const seed = await bip39.mnemonicToSeed(mnemonic);
        return this.createSparkWalletFromSeed(seed);
    }
    async getTrackedPublicKeys() {
        return Array.from(this.publicKeyToPrivateKeyMap.keys()).map(hexToBytes);
    }
    async generatePublicKey(hash) {
        if (!this.identityPrivateKey) {
            throw new Error("Private key is not set");
        }
        let newPrivateKey = null;
        if (hash) {
            newPrivateKey = this.deriveSigningKey(hash);
        }
        else {
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
    async removePublicKey(publicKey) {
        this.publicKeyToPrivateKeyMap.delete(bytesToHex(publicKey));
    }
    async subtractPrivateKeysGivenPublicKeys(first, second) {
        const firstPubKeyHex = bytesToHex(first);
        const secondPubKeyHex = bytesToHex(second);
        const firstPrivateKeyHex = this.publicKeyToPrivateKeyMap.get(firstPubKeyHex);
        const secondPrivateKeyHex = this.publicKeyToPrivateKeyMap.get(secondPubKeyHex);
        if (!firstPrivateKeyHex || !secondPrivateKeyHex) {
            throw new Error("Private key is not set");
        }
        const firstPrivateKey = hexToBytes(firstPrivateKeyHex);
        const secondPrivateKey = hexToBytes(secondPrivateKeyHex);
        const resultPrivKey = subtractPrivateKeys(firstPrivateKey, secondPrivateKey);
        const resultPubKey = secp256k1.getPublicKey(resultPrivKey);
        const resultPrivKeyHex = bytesToHex(resultPrivKey);
        const resultPubKeyHex = bytesToHex(resultPubKey);
        this.publicKeyToPrivateKeyMap.set(resultPubKeyHex, resultPrivKeyHex);
        return resultPubKey;
    }
    async splitSecretWithProofs({ secret, curveOrder, threshold, numShares, isSecretPubkey = false, }) {
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
    async signFrost({ message, privateAsPubKey, publicKey, verifyingKey, selfCommitment, statechainCommitments, adaptorPubKey, }) {
        const privateAsPubKeyHex = bytesToHex(privateAsPubKey);
        const signingPrivateKey = this.publicKeyToPrivateKeyMap.get(privateAsPubKeyHex);
        if (!signingPrivateKey) {
            throw new Error("Private key is not set");
        }
        const nonce = this.commitmentToNonceMap.get(selfCommitment);
        if (!nonce) {
            throw new Error("Nonce is not set");
        }
        const keyPackage = new KeyPackage(hexToBytes(signingPrivateKey), publicKey, verifyingKey);
        return signFrost({
            msg: message,
            keyPackage,
            nonce: createWasmSigningNonce(nonce),
            selfCommitment: createWasmSigningCommitment(selfCommitment),
            statechainCommitments,
            adaptorPubKey,
        });
    }
    async aggregateFrost({ message, publicKey, verifyingKey, selfCommitment, statechainCommitments, adaptorPubKey, selfSignature, statechainSignatures, statechainPublicKeys, }) {
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
    async createSparkWalletFromSeed(seed) {
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
    async signMessageWithPublicKey(message, publicKey, compact) {
        const privateKey = this.publicKeyToPrivateKeyMap.get(bytesToHex(publicKey));
        if (!privateKey) {
            throw new Error(`No private key found for public key: ${bytesToHex(publicKey)}`);
        }
        const signature = secp256k1.sign(message, hexToBytes(privateKey));
        if (compact) {
            return signature.toCompactRawBytes();
        }
        return signature.toDERRawBytes();
    }
    async signMessageWithIdentityKey(message, compact) {
        if (!this.identityPrivateKey?.privateKey) {
            throw new Error("Private key is not set");
        }
        const signature = secp256k1.sign(message, this.identityPrivateKey.privateKey);
        if (compact) {
            return signature.toCompactRawBytes();
        }
        return signature.toDERRawBytes();
    }
    async encryptLeafPrivateKeyEcies(receiverPublicKey, publicKey) {
        const publicKeyHex = bytesToHex(publicKey);
        const privateKey = this.publicKeyToPrivateKeyMap.get(publicKeyHex);
        if (!privateKey) {
            throw new Error("Private key is not set");
        }
        return ecies.encrypt(receiverPublicKey, hexToBytes(privateKey));
    }
    async decryptEcies(ciphertext) {
        if (!this.identityPrivateKey?.privateKey) {
            throw new Error("Private key is not set");
        }
        const receiverEciesPrivKey = ecies.PrivateKey.fromHex(bytesToHex(this.identityPrivateKey.privateKey));
        const privateKey = ecies.decrypt(receiverEciesPrivKey.toHex(), ciphertext);
        const publicKey = secp256k1.getPublicKey(privateKey);
        const publicKeyHex = bytesToHex(publicKey);
        const privateKeyHex = bytesToHex(privateKey);
        this.publicKeyToPrivateKeyMap.set(publicKeyHex, privateKeyHex);
        return publicKey;
    }
    async getRandomSigningCommitment() {
        const nonce = getRandomSigningNonce();
        const commitment = getSigningCommitmentFromNonce(nonce);
        this.commitmentToNonceMap.set(commitment, nonce);
        return commitment;
    }
    // Hardcode this for default ssp
    async getSspIdentityPublicKey() {
        return hexToBytes("028c094a432d46a0ac95349d792c2e3730bd60c29188db716f56a99e39b95338b4");
    }
    async hashRandomPrivateKey() {
        return sha256(secp256k1.utils.randomPrivateKey());
    }
    async generateAdaptorFromSignature(signature) {
        const adaptor = generateAdaptorFromSignature(signature);
        const adaptorPublicKey = secp256k1.getPublicKey(adaptor.adaptorPrivateKey);
        this.publicKeyToPrivateKeyMap.set(bytesToHex(adaptorPublicKey), bytesToHex(adaptor.adaptorPrivateKey));
        return {
            adaptorSignature: signature,
            adaptorPublicKey: adaptorPublicKey,
        };
    }
}
export { DefaultSparkSigner };
//# sourceMappingURL=signer.js.map