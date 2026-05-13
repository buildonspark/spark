import { describe, expect, it } from "@jest/globals";
import { secp256k1 } from "@noble/curves/secp256k1";
import { bytesToHex } from "@noble/curves/utils";
import { sha256 } from "@noble/hashes/sha2";
import { Transaction } from "@scure/btc-signer";

import { getP2TRScriptFromPublicKey } from "../utils/bitcoin.js";
import {
  createLightningHTLCTransaction,
  createReceiverSpendTx,
  createSenderSpendTx,
} from "../utils/htlc-transactions.js";
import { getNetwork, Network } from "../utils/network.js";

function randomKeypair() {
  const privateKey = secp256k1.utils.randomSecretKey();
  const publicKey = secp256k1.getPublicKey(privateKey);
  return { privateKey, publicKey };
}

function buildHtlcTx({
  receiverPubkey,
  senderPubkey,
  paymentHash,
}: {
  receiverPubkey: Uint8Array;
  senderPubkey: Uint8Array;
  paymentHash: Uint8Array;
}) {
  const network = getNetwork(Network.LOCAL);

  const nodeTx = new Transaction({
    version: 3,
    allowUnknownOutputs: true,
  });
  nodeTx.addInput({ txid: "00".repeat(32), index: 0 });
  nodeTx.addOutput({
    script: getP2TRScriptFromPublicKey(
      secp256k1.getPublicKey(secp256k1.utils.randomSecretKey()),
      Network.LOCAL,
    ),
    amount: 100_000n,
  });

  return createLightningHTLCTransaction({
    nodeTx,
    vout: 0,
    sequence: 0,
    hash: paymentHash,
    hashLockDestinationPubkey: receiverPubkey,
    sequenceLockDestinationPubkey: senderPubkey,
    applyFee: true,
    network,
  });
}

describe("createReceiverSpendTx", () => {
  it("pays the receiver hash-lock key, not the sender sequence-lock key", () => {
    const receiver = randomKeypair();
    const sender = randomKeypair();
    const preimage = new Uint8Array(32);
    preimage[0] = 0x11;
    const paymentHash = sha256(preimage);

    const htlcTx = buildHtlcTx({
      receiverPubkey: receiver.publicKey,
      senderPubkey: sender.publicKey,
      paymentHash,
    });

    const { spendTx } = createReceiverSpendTx({
      htlcTx,
      network: getNetwork(Network.LOCAL),
      hash: paymentHash,
      hashLockDestinationPubkey: receiver.publicKey,
      sequenceLockDestinationPubkey: sender.publicKey,
      fee: 1_000,
    });

    const output = spendTx.getOutput(0);
    const receiverScript = getP2TRScriptFromPublicKey(
      receiver.publicKey,
      Network.LOCAL,
    );
    const senderScript = getP2TRScriptFromPublicKey(
      sender.publicKey,
      Network.LOCAL,
    );

    expect(bytesToHex(output.script!)).toBe(bytesToHex(receiverScript));
    expect(bytesToHex(output.script!)).not.toBe(bytesToHex(senderScript));
  });
});

describe("createSenderSpendTx", () => {
  it("pays the sender sequence-lock key, not the receiver hash-lock key", () => {
    const receiver = randomKeypair();
    const sender = randomKeypair();
    const preimage = new Uint8Array(32);
    preimage[0] = 0x22;
    const paymentHash = sha256(preimage);

    const htlcTx = buildHtlcTx({
      receiverPubkey: receiver.publicKey,
      senderPubkey: sender.publicKey,
      paymentHash,
    });

    const { senderSpendTx } = createSenderSpendTx({
      htlcTx,
      network: getNetwork(Network.LOCAL),
      hash: paymentHash,
      hashLockDestinationPubkey: receiver.publicKey,
      sequenceLockDestinationPubkey: sender.publicKey,
      fee: 1_000,
    });

    const output = senderSpendTx.getOutput(0);
    const senderScript = getP2TRScriptFromPublicKey(
      sender.publicKey,
      Network.LOCAL,
    );
    const receiverScript = getP2TRScriptFromPublicKey(
      receiver.publicKey,
      Network.LOCAL,
    );

    expect(bytesToHex(output.script!)).toBe(bytesToHex(senderScript));
    expect(bytesToHex(output.script!)).not.toBe(bytesToHex(receiverScript));
  });
});
