import { describe, expect, it } from "@jest/globals";
import { bytesToHex, hexToBytes } from "@noble/curves/abstract/utils";
import { Transaction } from "@scure/btc-signer";
import {
  getP2TRAddressFromPkScript,
  getP2TRAddressFromPublicKey,
  getP2TRScriptFromPublicKey,
  getSigHashFromTx,
  getTxFromRawTxHex,
  getTxId,
} from "../utils/bitcoin.js";
import { Network } from "../utils/network.js";
import { ValidationError } from "../errors/types.js";

describe("bitcoin", () => {
  it("test p2tr address from public key", () => {
    const testVectors: {
      pubKey: string;
      p2trAddr: string;
      network: Network;
    }[] = [
      {
        pubKey:
          "0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798",
        p2trAddr:
          "bc1pmfr3p9j00pfxjh0zmgp99y8zftmd3s5pmedqhyptwy6lm87hf5sspknck9",
        network: Network.MAINNET,
      },
      {
        pubKey:
          "03797dd653040d344fd048c1ad05d4cbcb2178b30c6a0c4276994795f3e833da41",
        p2trAddr:
          "tb1p8dlmzllfah294ntwatr8j5uuvcj7yg0dete94ck2krrk0ka2c9qqex96hv",
        network: Network.TESTNET,
      },
    ];

    for (const tv of testVectors) {
      const pubKey = hexToBytes(tv.pubKey);
      const p2trAddr = getP2TRAddressFromPublicKey(pubKey, tv.network);
      expect(p2trAddr).toBe(tv.p2trAddr);
    }
  });

  it("test p2tr address from pk script", () => {
    const testVectors: {
      pkScript: string;
      p2trAddr: string;
      network: Network;
    }[] = [
      {
        pkScript:
          "51206d2a651074ff19686d4cd4e45aaaad3f85639e90bb24e21b875b174b0635eb30",
        p2trAddr:
          "bc1pd54x2yr5luvksm2v6nj9424d87zk885shvjwyxu8tvt5kp34avcq024v6k",
        network: Network.MAINNET,
      },
      {
        pkScript:
          "5120d0cd6fade9979fc9e0cc353d8e06a22f43d659cf09c8f909834e80468f4af966",
        p2trAddr:
          "bcrt1p6rxklt0fj70uncxvx57cup4z9apavkw0p8y0jzvrf6qydr62l9nqd94jkz",
        network: Network.REGTEST,
      },
    ];

    for (const tv of testVectors) {
      const pkScript = hexToBytes(tv.pkScript);
      const p2trAddr = getP2TRAddressFromPkScript(pkScript, tv.network);
      expect(p2trAddr).toBe(tv.p2trAddr);
    }
  });

  it("test tx from raw tx hex", () => {
    const rawTxHex =
      "02000000000102dc552c6c0ef5ed0d8cd64bd1d2d1ffd7cf0ec0b5ad8df2a4c6269b59cffcc696010000000000000000603fbd40e86ee82258c57571c557b89a444aabf5b6a05574e6c6848379febe9a00000000000000000002e86905000000000022512024741d89092c5965f35a63802352fa9c7fae4a23d471b9dceb3379e8ff6b7dd1d054080000000000220020aea091435e74e3c1eba0bd964e67a05f300ace9e73efa66fe54767908f3e68800140f607486d87f59af453d62cffe00b6836d8cca2c89a340fab5fe842b20696908c77fd2f64900feb0cbb1c14da3e02271503fc465fcfb1b043c8187dccdd494558014067dff0f0c321fc8abc28bf555acfdfa5ee889b6909b24bc66cedf05e8cc2750a4d95037c3dc9c24f1e502198bade56fef61a2504809f5b2a60a62afeaf8bf52e00000000";
    const tx = getTxFromRawTxHex(rawTxHex);
    expect(tx).toBeDefined();
  });

  it("test sig hash from tx", () => {
    const prevTx = getTxFromRawTxHex(
      "020000000001010cb9feccc0bdaac30304e469c50b4420c13c43d466e13813fcf42a73defd3f010000000000ffffffff018038010000000000225120d21e50e12ae122b4a5662c09b67cec7449c8182913bc06761e8b65f0fa2242f701400536f9b7542799f98739eeb6c6adaeb12d7bd418771bc5c6847f2abd19297bd466153600af26ccf0accb605c11ad667c842c5713832af4b7b11f1bcebe57745900000000",
    );

    const tx = new Transaction();

    tx.addInput({
      txid: getTxId(prevTx),
      index: 0,
    });

    const prevOutScript = prevTx.getOutput(0).script;
    if (!prevOutScript)
      throw new ValidationError("No script found in prevOut", {
        field: "prevOutScript",
      });

    tx.addOutput({
      script: prevOutScript,
      amount: 70000n,
    });

    const prevOut = prevTx.getOutput(0);
    if (!prevOut)
      throw new ValidationError("No output found in prevTx", {
        field: "prevOut",
      });

    // Calculate sighash
    const sighash = getSigHashFromTx(tx, 0, prevOut);

    expect(bytesToHex(sighash)).toBe(
      "8da5e7aa2b03491d7c2f4359ea4968dd58f69adf9af1a2c6881be0295591c293",
    );
  });

  it("test script from pubkey", () => {
    const pubKey =
      "03ef261da8259f5ea86abe1b7d658ebd13fe2b2ce2418dabe854ccec67bdb9ba87";
    const script = getP2TRScriptFromPublicKey(
      hexToBytes(pubKey),
      Network.MAINNET,
    );
    expect(bytesToHex(script)).toEqual(
      "51208af8e5e92783248418d5c68007dc8659a2100261b5bb561efc28dde94ec8cb93",
    );
  });
});
