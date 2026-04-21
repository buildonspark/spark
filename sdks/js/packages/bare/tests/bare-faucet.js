/**
 * Bare-compatible Bitcoin faucet for integration tests.
 *
 * Adapted from @buildonspark/spark-sdk test-faucet.ts. Cannot import the
 * original because spark-sdk/test-utils transitively pulls in Node.js code.
 *
 * IMPORTANT: require("@buildonspark/bare") must be called before this module
 * so that globalThis.fetch and globalThis.Headers are available.
 */

const { imports } = require("./utils.js");
const { secp256k1, schnorr } = require("@noble/curves/secp256k1", imports);
const {
  bytesToHex,
  hexToBytes,
} = require("@noble/curves/abstract/utils", imports);
const { sha256 } = require("@noble/hashes/sha2", imports);
const { concatBytes, utf8ToBytes } = require("@noble/hashes/utils", imports);
const btc = require("@scure/btc-signer", imports);
const { taprootTweakPrivKey } = require("@scure/btc-signer/utils", imports);

// Regtest network config (matches spark-sdk's NetworkConfig[Network.LOCAL])
const REGTEST_NETWORK = { ...btc.TEST_NETWORK, bech32: "bcrt" };

// Static keys for deterministic testing (same as spark-sdk test-faucet)
const STATIC_FAUCET_KEY = hexToBytes(
  "deadbeef1337cafe4242424242424242deadbeef1337cafe4242424242424242",
);
const STATIC_MINING_KEY = hexToBytes(
  "1337cafe4242deadbeef4242424242421337cafe4242deadbeef424242424242",
);

const SATS_PER_BTC = 100_000_000;
const REFILL_AMOUNT = 10_000_000n;
const COIN_AMOUNT = 1_000_000n;
const FEE_AMOUNT = 1000n;
const TARGET_NUM_COINS = 20;
const DEFAULT_FAUCET_NAMESPACE = "bare";

function getFaucetNamespace() {
  const suffix =
    process.env.SPARK_TEST_FAUCET_NAMESPACE ||
    (process.env.GITHUB_RUN_ID
      ? `${process.env.GITHUB_RUN_ID}-${process.env.GITHUB_RUN_ATTEMPT || "1"}`
      : "local");
  return `${DEFAULT_FAUCET_NAMESPACE}:${suffix}`;
}

function derivePrivateKey(baseKey, namespace, role) {
  let material = concatBytes(baseKey, utf8ToBytes(`|${namespace}|${role}|v1`));
  for (let i = 0; i < 1024; i++) {
    const candidate = sha256(material);
    if (secp256k1.utils.isValidPrivateKey(candidate)) {
      return candidate;
    }
    material = concatBytes(candidate, utf8ToBytes(`|retry:${i}`));
  }
  throw new Error("Failed to derive a valid secp256k1 private key");
}

// Bitcoin utility helpers (inlined from spark-sdk utils/bitcoin.ts)

function getP2TRAddressFromPublicKey(pubKey, network) {
  const internalKey = secp256k1.ProjectivePoint.fromHex(pubKey);
  const address = btc.p2tr(
    internalKey.toRawBytes().slice(1, 33),
    undefined,
    network,
  ).address;
  if (!address) throw new Error("Failed to get P2TR address");
  return address;
}

function getP2TRScriptFromPublicKey(pubKey, network) {
  const internalKey = secp256k1.ProjectivePoint.fromHex(pubKey);
  const script = btc.p2tr(
    internalKey.toRawBytes().slice(1, 33),
    undefined,
    network,
  ).script;
  if (!script) throw new Error("Failed to get P2TR script");
  return script;
}

function getP2TRAddressFromPkScript(script, network) {
  const decoded = btc.OutScript.decode(script);
  if (decoded.type === "tr") {
    const address = btc.Address(network).encode({
      type: "tr",
      pubkey: decoded.pubkey,
    });
    return address;
  }
  return null;
}

class BitcoinFaucet {
  static _instance = null;

  constructor(url, username, password) {
    this._url = url;
    this._username = username;
    this._password = password;
    this._coins = [];
    this._lock = Promise.resolve();
    const namespace = getFaucetNamespace();
    this._faucetKey = derivePrivateKey(STATIC_FAUCET_KEY, namespace, "faucet");
    this._miningKey = derivePrivateKey(STATIC_MINING_KEY, namespace, "miner");
    this._miningAddress = getP2TRAddressFromPublicKey(
      secp256k1.getPublicKey(this._miningKey),
      REGTEST_NETWORK,
    );
  }

  static getInstance() {
    if (!BitcoinFaucet._instance) {
      const localIngressHost = process.env.SPARK_LOCAL_INGRESS_HOST;
      const url =
        process.env.BITCOIN_RPC_URL ||
        (localIngressHost
          ? `http://${localIngressHost}:8332`
          : "http://127.0.0.1:8332");
      const username = process.env.BITCOIN_RPC_USER || "testutil";
      const password = process.env.BITCOIN_RPC_PASSWORD || "testutilpassword";
      BitcoinFaucet._instance = new BitcoinFaucet(url, username, password);
    }
    return BitcoinFaucet._instance;
  }

  async _withLock(operation) {
    const current = this._lock;
    let resolve;
    this._lock = new Promise((r) => (resolve = r));
    await current;
    try {
      return await operation();
    } finally {
      resolve();
    }
  }

  async fund() {
    return this._withLock(async () => {
      if (this._coins.length === 0) {
        await this._refill();
      }
      const coin = this._coins[0];
      if (!coin) throw new Error("Failed to get coin from faucet");
      this._coins = this._coins.slice(1);
      return coin;
    });
  }

  async _refill() {
    const minerPubKey = secp256k1.getPublicKey(this._miningKey);
    const address = getP2TRAddressFromPublicKey(minerPubKey, REGTEST_NETWORK);

    const scanResult = await this._callWithRetry("scantxoutset", [
      "start",
      [`addr(${address})`],
    ]);

    let selectedUtxo;
    let selectedUtxoAmountSats;

    if (scanResult.success && scanResult.unspents.length > 0) {
      selectedUtxo = scanResult.unspents.find((utxo) => {
        const isValueEnough =
          BigInt(Math.floor(utxo.amount * SATS_PER_BTC)) >=
          COIN_AMOUNT + FEE_AMOUNT;
        const isMature = scanResult.height - utxo.height >= 100;
        return isValueEnough && isMature;
      });

      if (selectedUtxo) {
        selectedUtxoAmountSats = BigInt(
          Math.floor(selectedUtxo.amount * SATS_PER_BTC),
        );
      }
    }

    if (!selectedUtxo) {
      const fundingTxid = await this._sendToAddressInternal(
        address,
        Number(REFILL_AMOUNT),
      );
      await this.generateToAddress(1, address);

      const fundingTxRaw = await this._getRawTransaction(fundingTxid);
      const fundingTx = btc.Transaction.fromRaw(hexToBytes(fundingTxRaw.hex));

      for (let i = 0; i < fundingTx.outputsLength; i++) {
        const output = fundingTx.getOutput(i);
        if (!output.script || !output.amount) continue;

        const outputAddress = getP2TRAddressFromPkScript(
          output.script,
          REGTEST_NETWORK,
        );

        if (outputAddress === address && output.amount === REFILL_AMOUNT) {
          selectedUtxo = { txid: fundingTxid, vout: i, amount: REFILL_AMOUNT };
          selectedUtxoAmountSats = REFILL_AMOUNT;
          break;
        }
      }
    }

    if (!selectedUtxo) {
      throw new Error("No UTXO large enough to create even one faucet coin");
    }

    const maxPossibleCoins = Number(
      (selectedUtxoAmountSats - FEE_AMOUNT) / COIN_AMOUNT,
    );
    const numCoinsToCreate = Math.min(maxPossibleCoins, TARGET_NUM_COINS);

    if (numCoinsToCreate < 1) {
      throw new Error(
        `Selected UTXO (${selectedUtxoAmountSats} sats) too small for a faucet coin of ${COIN_AMOUNT} sats`,
      );
    }

    const splitTx = new btc.Transaction();
    splitTx.addInput({
      txid: selectedUtxo.txid,
      index: selectedUtxo.vout,
    });

    const faucetPubKey = secp256k1.getPublicKey(this._faucetKey);
    const script = getP2TRScriptFromPublicKey(faucetPubKey, REGTEST_NETWORK);
    for (let i = 0; i < numCoinsToCreate; i++) {
      splitTx.addOutput({ script, amount: COIN_AMOUNT });
    }

    const remainingValue =
      selectedUtxoAmountSats -
      COIN_AMOUNT * BigInt(numCoinsToCreate) -
      FEE_AMOUNT;
    const minerScript = getP2TRScriptFromPublicKey(
      secp256k1.getPublicKey(this._miningKey),
      REGTEST_NETWORK,
    );
    if (remainingValue > 0n) {
      splitTx.addOutput({ script: minerScript, amount: remainingValue });
    }

    const signedSplitTx = await this.signFaucetCoin(
      splitTx,
      { amount: selectedUtxoAmountSats },
      this._miningKey,
    );

    await this.broadcastTx(bytesToHex(signedSplitTx.extract()));

    const splitTxId = signedSplitTx.id;
    for (let i = 0; i < numCoinsToCreate; i++) {
      this._coins.push({
        key: this._faucetKey,
        outpoint: { txid: hexToBytes(splitTxId), index: i },
        txout: signedSplitTx.getOutput(i),
      });
    }
  }

  async signFaucetCoin(unsignedTx, fundingTxOut, key) {
    const pubKey = secp256k1.getPublicKey(key);
    const internalKey = pubKey.slice(1);
    const script = getP2TRScriptFromPublicKey(pubKey, REGTEST_NETWORK);

    unsignedTx.updateInput(0, {
      tapInternalKey: internalKey,
      witnessUtxo: { script, amount: fundingTxOut.amount },
    });

    const sighash = unsignedTx.preimageWitnessV1(
      0,
      new Array(unsignedTx.inputsLength).fill(script),
      btc.SigHash.DEFAULT,
      new Array(unsignedTx.inputsLength).fill(fundingTxOut.amount),
    );

    const merkleRoot = new Uint8Array();
    const tweakedKey = taprootTweakPrivKey(key, merkleRoot);
    if (!tweakedKey)
      throw new Error("Invalid private key for taproot tweaking");

    const signature = schnorr.sign(sighash, tweakedKey);
    unsignedTx.updateInput(0, { tapKeySig: signature });
    unsignedTx.finalize();
    return unsignedTx;
  }

  async sendToAddress(address, amount, blocksToGenerate = 1) {
    const amountBtc = Number(amount) / SATS_PER_BTC;
    const txid = await this._call("sendtoaddress", [address, amountBtc]);

    if (blocksToGenerate > 0) {
      const miningAddress = await this.getNewAddress();
      await this.generateToAddress(blocksToGenerate, miningAddress);
    }

    const rawTx = await this._getRawTransaction(txid);
    return btc.Transaction.fromRaw(hexToBytes(rawTx.hex));
  }

  async mineBlocks(numBlocks) {
    return await this.generateToAddress(numBlocks, this._miningAddress);
  }

  async generateToAddress(numBlocks, address) {
    return await this._call("generatetoaddress", [numBlocks, address]);
  }

  async broadcastTx(txHex) {
    return await this._call("sendrawtransaction", [txHex, 0]);
  }

  async getNewAddress() {
    const key = secp256k1.utils.randomPrivateKey();
    const pubKey = secp256k1.getPublicKey(key);
    return getP2TRAddressFromPublicKey(pubKey, REGTEST_NETWORK);
  }

  async _sendToAddressInternal(address, amountSats) {
    return await this._call("sendtoaddress", [
      address,
      amountSats / SATS_PER_BTC,
    ]);
  }

  async _getRawTransaction(txid) {
    return await this._call("getrawtransaction", [txid, 2]);
  }

  async _callWithRetry(
    method,
    params,
    { maxAttempts = 5, baseDelayMs = 200, maxDelayMs = 3000 } = {},
  ) {
    for (let attempt = 1; attempt <= maxAttempts; attempt++) {
      try {
        return await this._call(method, params);
      } catch (err) {
        const isRetryable = err.message?.includes("Scan already in progress");
        if (!isRetryable || attempt === maxAttempts) throw err;
        const delay =
          Math.min(baseDelayMs * 2 ** (attempt - 1), maxDelayMs) +
          Math.random() * 100;
        await new Promise((r) => setTimeout(r, delay));
      }
    }
  }

  async _call(method, params) {
    const response = await globalThis.fetch(this._url, {
      method: "POST",
      headers: new globalThis.Headers({
        "Content-Type": "application/json",
        Authorization: "Basic " + btoa(`${this._username}:${this._password}`),
      }),
      body: JSON.stringify({
        jsonrpc: "1.0",
        id: "spark-bare-test",
        method,
        params,
      }),
    });

    const data = await response.json();
    if (data.error) {
      console.error(`RPC Error for method ${method}:`, data.error);
      throw new Error(
        `Bitcoin RPC error: ${method} - ${JSON.stringify(data.error)}`,
      );
    }
    return data.result;
  }
}

module.exports = { BitcoinFaucet };
