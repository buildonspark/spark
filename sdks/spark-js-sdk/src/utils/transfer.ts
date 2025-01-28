import {
  bytesToHex,
  bytesToNumberBE,
  equalBytes,
  numberToBytesBE,
} from "@noble/curves/abstract/utils";
import {
  ClaimLeafKeyTweak,
  LeafRefundTxSigningJob,
  LeafRefundTxSigningResult,
  NodeSignatures,
  SendLeafKeyTweak,
  Transfer,
  TreeNode,
} from "../proto/spark";
import { subtractPrivateKeys } from "./keys";
import { splitSecretWithProofs, VerifiableSecretShare } from "./secret-sharing";
import { secp256k1 } from "@noble/curves/secp256k1";
import * as ecies from "eciesjs";
import { sha256 } from "@scure/btc-signer/utils";
import { Config } from "../spark-sdk";
import { createNewGrpcConnection } from "./connection";
import {
  getP2TRAddressFromPublicKey,
  getSigHashFromTx,
  getTxFromRawTxBytes,
} from "./bitcoin";
import {
  copySigningCommitment,
  getRandomSigningNonce,
  getSigningCommitmentFromNonce,
} from "./signing";
import { Transaction } from "@scure/btc-signer";
import { KeyPackage, SigningNonce } from "../wasm/spark_bindings";
import { NetworkConfig } from "./network";
import * as btc from "@scure/btc-signer";
import { aggregateFrost, encryptEcies, signFrost } from "./wasm";
import { randomUUID } from "crypto";
import { SignatureIntent } from "../proto/common";

const TIME_LOCK_INTERVAL = 100;

export type LeafKeyTweak = {
  leaf: TreeNode;
  signingPrivKey: Uint8Array;
  newSigningPrivKey: Uint8Array;
};

export type ClaimLeafData = {
  signingPrivKey: Uint8Array;
  tx?: Transaction;
  refundTx?: Transaction;
  nonce: SigningNonce;
  vout?: number;
};

export type LeafRefundSigningData = {
  signingPrivKey: Uint8Array;
  receivingPubkey: Uint8Array;
  tx: Transaction;
  refundTx?: Transaction;
  nonce: SigningNonce;
  vout: number;
};

export async function sendTransferTweakKey(
  config: Config,
  transfer: Transfer,
  leaves: LeafKeyTweak[],
  refundSignatureMap: Map<string, Uint8Array>
): Promise<Transfer> {
  const keyTweakInputMap = prepareSendTransferKeyTweaks(
    config,
    transfer,
    leaves,
    refundSignatureMap
  );

  let updatedTransfer: Transfer | undefined;
  const errors: Error[] = [];
  const promises = Object.entries(config.signingOperators).map(
    async ([identifier, operator]) => {
      const sparkClient = createNewGrpcConnection(operator.address);

      const leavesToSend = keyTweakInputMap.get(identifier);
      if (!leavesToSend) {
        errors.push(new Error(`No leaves to send for operator ${identifier}`));
        return;
      }
      try {
        const transferResp = await sparkClient.complete_send_transfer({
          transferId: transfer.id,
          ownerIdentityPublicKey: secp256k1.getPublicKey(
            config.identityPrivateKey
          ),
          leavesToSend,
        });

        if (!updatedTransfer) {
          updatedTransfer = transferResp.transfer;
        } else {
          if (!transferResp.transfer) {
            errors.push(
              new Error(`No transfer response from operator ${identifier}`)
            );
            return;
          }

          if (!compareTransfers(updatedTransfer, transferResp.transfer)) {
            errors.push(
              new Error(`Inconsistent transfer response from operators`)
            );
          }
        }
      } catch (e) {
        errors.push(
          new Error(`Failed to send leaves to operator ${identifier}: ${e}`)
        );
      }

      sparkClient.close?.();
    }
  );

  await Promise.all(promises);

  if (errors.length > 0) {
    throw errors[0];
  }

  if (!updatedTransfer) {
    throw new Error("No updated transfer found");
  }

  return updatedTransfer;
}

export async function sendTransferSignRefund(
  config: Config,
  leaves: LeafKeyTweak[],
  receiverIdentityPubkey: Uint8Array,
  expiryTime: Date
): Promise<{ transfer: Transfer; signatureMap: Map<string, Uint8Array> }> {
  const transferID = randomUUID();

  const leafDataMap = new Map<string, LeafRefundSigningData>();
  for (const leaf of leaves) {
    const nonce = getRandomSigningNonce();
    const tx = getTxFromRawTxBytes(leaf.leaf.nodeTx);
    const refundTx = getTxFromRawTxBytes(leaf.leaf.refundTx);
    leafDataMap.set(leaf.leaf.id, {
      signingPrivKey: leaf.signingPrivKey,
      receivingPubkey: receiverIdentityPubkey,
      nonce,
      tx,
      refundTx,
      vout: leaf.leaf.vout,
    });
  }

  const signingJobs = prepareRefundSoSigningJobs(leaves, config, leafDataMap);

  const sparkConn = createNewGrpcConnection(
    config.signingOperators[config.coodinatorIdentifier].address
  );

  const response = await sparkConn.start_send_transfer({
    transferId: transferID,
    leavesToSend: signingJobs,
    ownerIdentityPublicKey: secp256k1.getPublicKey(
      config.identityPrivateKey,
      true
    ),
    receiverIdentityPublicKey: receiverIdentityPubkey,
    expiryTime: expiryTime,
  });

  sparkConn.close?.();

  const signatures = await signRefunds(leafDataMap, response.signingResults);

  const signatureMap = new Map<string, Uint8Array>();
  for (const signature of signatures) {
    signatureMap.set(signature.nodeId, signature.refundTxSignature);
  }

  if (!response.transfer) {
    throw new Error("No transfer response from coordinator");
  }

  return {
    transfer: response.transfer,
    signatureMap,
  };
}

export function prepareSendTransferKeyTweaks(
  config: Config,
  transfer: Transfer,
  leaves: LeafKeyTweak[],
  refundSignatureMap: Map<string, Uint8Array>
): Map<string, SendLeafKeyTweak[]> {
  const receiverEciesPubKey = ecies.PublicKey.fromHex(
    bytesToHex(transfer.receiverIdentityPublicKey)
  );

  const leavesTweaksMap = new Map<string, SendLeafKeyTweak[]>();

  for (const leaf of leaves) {
    const refundSignature = refundSignatureMap.get(leaf.leaf.id);
    if (!refundSignature) {
      throw new Error(`Refund signature not found for leaf ${leaf.leaf.id}`);
    }
    const leafTweaksMap = prepareSingleSendTransferKeyTweak(
      config,
      transfer.id,
      leaf,
      receiverEciesPubKey,
      refundSignature
    );
    for (const [identifier, leafTweak] of leafTweaksMap) {
      leavesTweaksMap.set(identifier, [
        ...(leavesTweaksMap.get(identifier) || []),
        leafTweak,
      ]);
    }
  }

  return leavesTweaksMap;
}

export function prepareSingleSendTransferKeyTweak(
  config: Config,
  transferID: string,
  leaf: LeafKeyTweak,
  receiverEciesPubKey: ecies.PublicKey,
  refundSignature: Uint8Array
): Map<string, SendLeafKeyTweak> {
  const privKeyTweak = subtractPrivateKeys(
    leaf.signingPrivKey,
    leaf.newSigningPrivKey
  );

  const shares = splitSecretWithProofs(
    bytesToNumberBE(privKeyTweak),
    secp256k1.CURVE.n,
    config.threshold,
    Object.keys(config.signingOperators).length
  );

  const pubkeySharesTweak = new Map<string, Uint8Array>();
  for (const [identifier, operator] of Object.entries(
    config.signingOperators
  )) {
    const share = findShare(shares, operator.id);
    if (!share) {
      throw new Error(`Share not found for operator ${operator.id}`);
    }

    // TODO: check if this is correct
    const pubkeyTweak = secp256k1.getPublicKey(
      numberToBytesBE(share.share, 32),
      true
    );
    pubkeySharesTweak.set(identifier, pubkeyTweak);
  }

  const secretCipher = ecies.encrypt(
    receiverEciesPubKey.toBytes(),
    leaf.newSigningPrivKey
  );

  const encoder = new TextEncoder();
  const payload = new Uint8Array([
    ...encoder.encode(leaf.leaf.id),
    ...encoder.encode(transferID),
    ...secretCipher,
  ]);

  const payloadHash = sha256(payload);
  const signature = secp256k1
    .sign(payloadHash, config.identityPrivateKey)
    .toCompactRawBytes();

  const leafTweaksMap = new Map<string, SendLeafKeyTweak>();
  for (const [identifier, operator] of Object.entries(
    config.signingOperators
  )) {
    const share = findShare(shares, operator.id);
    if (!share) {
      throw new Error(`Share not found for operator ${operator.id}`);
    }

    leafTweaksMap.set(identifier, {
      leafId: leaf.leaf.id,
      secretShareTweak: {
        secretShare: numberToBytesBE(share.share, 32),
        proofs: share.proofs,
      },
      pubkeySharesTweak: Object.fromEntries(pubkeySharesTweak),
      secretCipher,
      signature,
      refundSignature,
    });
  }

  return leafTweaksMap;
}

export function prepareRefundSoSigningJobs(
  leaves: LeafKeyTweak[],
  config: Config,
  leafDataMap: Map<string, LeafRefundSigningData>
): LeafRefundTxSigningJob[] {
  const signingJobs: LeafRefundTxSigningJob[] = [];
  for (const leaf of leaves) {
    const refundSigningData = leafDataMap.get(leaf.leaf.id);
    if (!refundSigningData) {
      throw new Error(`Leaf data not found for leaf ${leaf.leaf.id}`);
    }
    const signingPubkey = secp256k1.getPublicKey(
      refundSigningData.signingPrivKey
    );
    const refundTx = createRefundTx(
      config,
      leaf.leaf,
      refundSigningData.receivingPubkey
    );

    refundSigningData.refundTx = refundTx;

    const refundNonceCommitmentProto = getSigningCommitmentFromNonce(
      refundSigningData.nonce
    );

    signingJobs.push({
      leafId: leaf.leaf.id,
      refundTxSigningJob: {
        signingPublicKey: signingPubkey,
        rawTx: refundTx.toBytes(),
        signingNonceCommitment: refundNonceCommitmentProto,
      },
    });
  }

  return signingJobs;
}

export function createRefundTx(
  config: Config,
  leaf: TreeNode,
  receivingPubkey: Uint8Array
): Transaction {
  const tx = getTxFromRawTxBytes(leaf.nodeTx);
  const refundTx = getTxFromRawTxBytes(leaf.refundTx);

  const newRefundTx = new Transaction();
  const sequence =
    (1 << 30) |
    ((refundTx.getInput(0).sequence || 0) & (0xffff - TIME_LOCK_INTERVAL));
  newRefundTx.addInput({
    txid: tx.id,
    index: 0,
    sequence,
  });

  const finalScriptSig = refundTx.getInput(0).finalScriptSig;
  if (!finalScriptSig) {
    throw new Error(`Final script sig not found for refund tx`);
  }

  const refundP2trAddress = getP2TRAddressFromPublicKey(
    receivingPubkey,
    config.network
  );
  const refundAddress = btc
    .Address(NetworkConfig[config.network])
    .decode(refundP2trAddress);
  const refundPkScript = btc.OutScript.encode(refundAddress);

  const amount = refundTx.getOutput(0).amount;
  if (!amount) {
    throw new Error(`Amount not found for refund tx`);
  }
  newRefundTx.addOutput({
    script: refundPkScript,
    amount,
  });

  newRefundTx.updateInput(0, {
    finalScriptSig,
  });

  return newRefundTx;
}

export async function claimTransferTweakKeys(
  transfer: Transfer,
  leaves: LeafKeyTweak[],
  config: Config
) {
  const leavesTweaksMap = prepareClaimLeavesKeyTweaks(leaves, config);

  const errors: Error[] = [];

  const promises = Object.entries(config.signingOperators).map(
    async ([identifier, operator]) => {
      const sparkClient = createNewGrpcConnection(operator.address);

      const leavesToReceive = leavesTweaksMap.get(identifier);
      if (!leavesToReceive) {
        errors.push(
          new Error(`No leaves to receive for operator ${identifier}`)
        );
        return;
      }

      await sparkClient.claim_transfer_tweak_keys({
        transferId: transfer.id,
        ownerIdentityPublicKey: secp256k1.getPublicKey(
          config.identityPrivateKey
        ),
        leavesToReceive,
      });
      sparkClient.close?.();
    }
  );

  await Promise.all(promises);

  if (errors.length > 0) {
    throw errors[0];
  }
}

export function prepareClaimLeavesKeyTweaks(
  leaves: LeafKeyTweak[],
  config: Config
): Map<string, ClaimLeafKeyTweak[]> {
  const leafDataMap = new Map<string, ClaimLeafKeyTweak[]>();
  for (const leaf of leaves) {
    const leafData = prepareClaimLeafKeyTweaks(leaf, config);
    for (const [identifier, leafTweak] of leafData) {
      leafDataMap.set(identifier, [
        ...(leafDataMap.get(identifier) || []),
        leafTweak,
      ]);
    }
  }
  return leafDataMap;
}

export function prepareClaimLeafKeyTweaks(
  leaf: LeafKeyTweak,
  config: Config
): Map<string, ClaimLeafKeyTweak> {
  const prvKeyTweak = subtractPrivateKeys(
    leaf.signingPrivKey,
    leaf.newSigningPrivKey
  );
  const shares = splitSecretWithProofs(
    bytesToNumberBE(prvKeyTweak),
    secp256k1.CURVE.n,
    config.threshold,
    Object.keys(config.signingOperators).length
  );
  const pubkeySharesTweak = new Map<string, Uint8Array>();

  for (const [identifier, operator] of Object.entries(
    config.signingOperators
  )) {
    const share = findShare(shares, operator.id);
    if (!share) {
      throw new Error(`Share not found for operator ${operator.id}`);
    }
    const pubkeyTweak = secp256k1.getPublicKey(
      numberToBytesBE(share.share, 32)
    );
    pubkeySharesTweak.set(identifier, pubkeyTweak);
  }

  const leafTweaksMap = new Map<string, ClaimLeafKeyTweak>();
  for (const [identifier, operator] of Object.entries(
    config.signingOperators
  )) {
    const share = findShare(shares, operator.id);
    if (!share) {
      throw new Error(`Share not found for operator ${operator.id}`);
    }

    leafTweaksMap.set(identifier, {
      leafId: leaf.leaf.id,
      secretShareTweak: {
        secretShare: numberToBytesBE(share.share, 32),
        proofs: share.proofs,
      },
      pubkeySharesTweak: Object.fromEntries(pubkeySharesTweak),
    });
  }

  return leafTweaksMap;
}

export async function claimTransferSignRefunds(
  transfer: Transfer,
  leafKeys: LeafKeyTweak[],
  config: Config
): Promise<NodeSignatures[]> {
  const leafDataMap: Map<string, LeafRefundSigningData> = new Map();
  for (const leafKey of leafKeys) {
    const nonce = getRandomSigningNonce();
    const tx = getTxFromRawTxBytes(leafKey.leaf.nodeTx);
    leafDataMap.set(leafKey.leaf.id, {
      signingPrivKey: leafKey.newSigningPrivKey,
      receivingPubkey: secp256k1.getPublicKey(leafKey.newSigningPrivKey),
      nonce,
      tx,
      vout: leafKey.leaf.vout,
    });
  }

  const signingJobs = prepareRefundSoSigningJobs(leafKeys, config, leafDataMap);

  const sparkClient = createNewGrpcConnection(
    config.signingOperators[config.coodinatorIdentifier].address
  );
  const resp = await sparkClient.claim_transfer_sign_refunds({
    transferId: transfer.id,
    ownerIdentityPublicKey: secp256k1.getPublicKey(config.identityPrivateKey),
    signingJobs,
  });
  sparkClient.close?.();
  return signRefunds(leafDataMap, resp.signingResults);
}

export async function signRefunds(
  leafDataMap: Map<string, ClaimLeafData>,
  operatorSigningResults: LeafRefundTxSigningResult[]
): Promise<NodeSignatures[]> {
  const nodeSignatures: NodeSignatures[] = [];
  for (const operatorSigningResult of operatorSigningResults) {
    const leafData = leafDataMap.get(operatorSigningResult.leafId);
    if (
      !leafData ||
      !leafData.tx ||
      leafData.vout === undefined ||
      !leafData.refundTx
    ) {
      throw new Error(
        `Leaf data not found for leaf ${operatorSigningResult.leafId}`
      );
    }

    const txOutput = leafData.tx?.getOutput(leafData.vout);
    if (!txOutput) {
      throw new Error(
        `Output not found for leaf ${operatorSigningResult.leafId}`
      );
    }

    const refundTxSighash = getSigHashFromTx(leafData.refundTx, 0, txOutput);
    const nonceCommitment = getSigningCommitmentFromNonce(leafData.nonce);
    const userKeyPackage = new KeyPackage(
      leafData.signingPrivKey,
      secp256k1.getPublicKey(leafData.signingPrivKey),
      operatorSigningResult.verifyingKey
    );

    const userSignature = signFrost({
      msg: refundTxSighash,
      keyPackage: userKeyPackage,
      nonce: leafData.nonce,
      selfCommitment: copySigningCommitment(nonceCommitment),
      statechainCommitments:
        operatorSigningResult.refundTxSigningResult?.signingNonceCommitments,
    });

    const refundAggregate = aggregateFrost({
      msg: refundTxSighash,
      statechainSignatures:
        operatorSigningResult.refundTxSigningResult?.signatureShares,
      statechainPublicKeys:
        operatorSigningResult.refundTxSigningResult?.publicKeys,
      verifyingKey: operatorSigningResult.verifyingKey,
      statechainCommitments:
        operatorSigningResult.refundTxSigningResult?.signingNonceCommitments,
      selfCommitment: copySigningCommitment(nonceCommitment),
      selfPublicKey: secp256k1.getPublicKey(leafData.signingPrivKey),
      selfSignature: userSignature,
    });

    nodeSignatures.push({
      nodeId: operatorSigningResult.leafId,
      refundTxSignature: refundAggregate,
      nodeTxSignature: new Uint8Array(),
    });
  }

  return nodeSignatures;
}

export async function finalizeTransfer(
  nodeSignatures: NodeSignatures[],
  coordinatorAddress: string
) {
  const sparkClient = createNewGrpcConnection(coordinatorAddress);
  await sparkClient.finalize_node_signatures({
    intent: SignatureIntent.TRANSFER,
    nodeSignatures,
  });
  sparkClient.close?.();
}

export function findShare(
  shares: VerifiableSecretShare[],
  operatorID: number
): VerifiableSecretShare | undefined {
  const targetShareIndex = BigInt(operatorID + 1);
  for (const s of shares) {
    if (s.index === targetShareIndex) {
      return s;
    }
  }
  return undefined;
}

export async function compareTransfers(
  transfer1: Transfer,
  transfer2: Transfer
): Promise<boolean> {
  return (
    transfer1.id === transfer2.id &&
    equalBytes(
      transfer1.senderIdentityPublicKey,
      transfer2.senderIdentityPublicKey
    ) &&
    transfer1.status === transfer2.status &&
    transfer1.totalValue === transfer2.totalValue &&
    transfer1.expiryTime?.getTime() === transfer2.expiryTime?.getTime() &&
    transfer1.leaves.length === transfer2.leaves.length
  );
}
