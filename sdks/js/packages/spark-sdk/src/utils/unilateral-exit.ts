// unilateral-exit.ts

import { bytesToHex, hexToBytes } from "@noble/curves/utils";
import { ripemd160 } from "@noble/hashes/legacy";
import { sha256 } from "@noble/hashes/sha2";
import * as btc from "@scure/btc-signer";
import * as psbt from "@scure/btc-signer/psbt";
import type { SparkServiceClient } from "../proto/spark.js";
import { TreeNode } from "../proto/spark.js";
import { getTxFromRawTxHex, getTxId } from "../utils/bitcoin.js";
import { isTxBroadcast } from "../utils/mempool.js";

// Types
export interface LeafInfo {
  leafId: string;
  nodeTx: string; // raw tx hex
  refundTx: string; // raw tx hex
  // Add other fields as needed
}

export interface Utxo {
  txid: string;
  vout: number;
  value: bigint;
  script: string;
  publicKey: string;
}

export interface FeeRate {
  satPerVbyte: number;
}

export interface FeeBumpTxPackage {
  tx: string;
  feeBumpPsbt?: string;
}

export interface FeeBumpTxChain {
  leafId: string;
  txPackages: FeeBumpTxPackage[];
}

export interface TxChain {
  leafId: string;
  transactions: string[];
}

export interface BroadcastConfig {
  bitcoinCoreRpcUrl?: string;
  rpcUsername?: string;
  rpcPassword?: string;
  autoBroadcast?: boolean;
  network?: "MAINNET" | "REGTEST" | "TESTNET" | "SIGNET" | "LOCAL";
}

export interface BroadcastResult {
  success: boolean;
  txids?: string[];
  error?: string;
  broadcastedPackages?: number;
}

// Helper function to convert WIF private key to hex
function wifToHex(wif: string): string {
  try {
    // WIF decoding using base58 (simplified version)
    const base58Alphabet =
      "123456789ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz";

    // Decode base58
    let decoded = BigInt(0);
    for (let i = 0; i < wif.length; i++) {
      const char = wif[i];
      if (!char) {
        throw new Error("Invalid character in WIF at position " + i);
      }
      const index = base58Alphabet.indexOf(char);
      if (index === -1) {
        throw new Error("Invalid character in WIF");
      }
      decoded = decoded * BigInt(58) + BigInt(index);
    }

    // Convert to hex and pad to ensure proper length
    let hex = decoded.toString(16);

    // WIF format: [version][32-byte private key][compression flag][4-byte checksum]
    // We want the 32-byte private key part (skip version byte, take 32 bytes)
    if (hex.length >= 74) {
      // 1 + 32 + 1 + 4 = 38 bytes = 76 hex chars minimum
      // Skip version byte (2 hex chars) and take 32 bytes (64 hex chars)
      const privateKeyHex = hex.substring(2, 66);
      return privateKeyHex;
    }

    throw new Error("Invalid WIF length");
  } catch (error) {
    throw new Error(`Failed to convert WIF to hex: ${error}`);
  }
}

export function isEphemeralAnchorOutput(
  script?: Uint8Array,
  amount?: bigint,
): boolean {
  return Boolean(
    amount === 0n &&
      script &&
      // Pattern 1: Bare OP_TRUE (single byte 0x51)
      ((script.length === 1 && script[0] === 0x51) ||
        // Pattern 2: Push OP_TRUE (two bytes 0x01 0x51) - MALFORMED but we detect it
        (script.length === 2 && script[0] === 0x01 && script[1] === 0x51) ||
        // Pattern 3: Bitcoin v29 ephemeral anchor script (7 bytes: 015152014e0173)
        (script.length === 7 &&
          script[0] === 0x01 &&
          script[1] === 0x51 &&
          script[2] === 0x52 &&
          script[3] === 0x01 &&
          script[4] === 0x4e &&
          script[5] === 0x01 &&
          script[6] === 0x73) ||
        // Pattern 4: Bitcoin ephemeral anchor OP_1 + push 2 bytes (4 bytes: 51024e73)
        (script.length === 4 &&
          script[0] === 0x51 &&
          script[1] === 0x02 &&
          script[2] === 0x4e &&
          script[3] === 0x73)),
  );
}

// Main function to generate unilateral exit tx chains for broadcasting non-CPFP transactions
export async function constructUnilateralExitTxs(
  nodeHexStrings: string[],
  sparkClient?: SparkServiceClient,
  network?: any, // Network enum from the proto
): Promise<TxChain[]> {
  const result: TxChain[] = [];

  // Convert hex strings to TreeNode objects
  const nodes = nodeHexStrings.map((hex) => TreeNode.decode(hexToBytes(hex)));

  // Create a map of nodes by ID for easy lookup
  const nodeMap = new Map<string, TreeNode>();
  for (const node of nodes) {
    nodeMap.set(node.id, node);
  }

  // For each provided node, build its complete chain to the root
  for (const node of nodes) {
    const transactions: string[] = [];

    // Build the chain from this node to the root
    const chain: TreeNode[] = [];
    let currentNode = node;

    // Walk up the chain to find the root, querying for each parent
    while (currentNode) {
      chain.unshift(currentNode);

      if (currentNode.parentNodeId) {
        // Check if we already have the parent in our map (from previous queries)
        let parentNode = nodeMap.get(currentNode.parentNodeId);

        if (!parentNode && sparkClient) {
          // Query for the parent node
          try {
            const response = await sparkClient.query_nodes({
              source: {
                $case: "nodeIds",
                nodeIds: {
                  nodeIds: [currentNode.parentNodeId],
                },
              },
              includeParents: true,
              network: network || 0, // Default to mainnet if not provided
            });

            parentNode = response.nodes[currentNode.parentNodeId];

            if (parentNode) {
              // Add to our map for future use
              nodeMap.set(currentNode.parentNodeId, parentNode);
            }
          } catch (error) {
            console.warn(
              `Failed to query parent node ${currentNode.parentNodeId}: ${error}`,
            );
            break;
          }
        }

        if (parentNode) {
          currentNode = parentNode;
        } else {
          if (!sparkClient) {
            console.warn(
              `Parent node ${currentNode.parentNodeId} not found. Provide a sparkClient to fetch missing parents.`,
            );
          } else {
            console.warn(
              `Parent node ${currentNode.parentNodeId} not found in database. Chain may be incomplete.`,
            );
          }
          break;
        }
      } else {
        // We've reached the root
        break;
      }
    }

    // Now walk down the chain from root to leaf to build transactions
    for (const chainNode of chain) {
      // Add node tx
      const nodeTx = bytesToHex(chainNode.nodeTx);
      transactions.push(nodeTx);

      // If this is the original node we started with, also add its refund tx
      if (chainNode.id === node.id) {
        const refundTx = bytesToHex(chainNode.refundTx);
        transactions.push(refundTx);
      }
    }

    result.push({
      leafId: node.id,
      transactions,
    });
  }

  return result;
}

// Main function to generate unilateral exit tx chains for broadcasting CPFP transactions
export async function constructUnilateralExitFeeBumpPackages(
  nodeHexStrings: string[],
  utxos: Utxo[],
  feeRate: FeeRate,
  electrsUrl: string,
  sparkClient?: SparkServiceClient,
  network?: any, // Network enum from the proto
): Promise<FeeBumpTxChain[]> {
  const result: FeeBumpTxChain[] = [];

  // Sort UTXOs by value in descending order and make a copy we can modify
  const availableUtxos = [...utxos].sort((a, b) => {
    if (a.value > b.value) return -1;
    if (a.value < b.value) return 1;
    return 0;
  });

  // Convert hex strings to TreeNode objects with better error handling
  const nodes: TreeNode[] = [];
  for (let i = 0; i < nodeHexStrings.length; i++) {
    const hex = nodeHexStrings[i];
    try {
      // Validate that this looks like a proper hex string
      if (!hex || hex.length === 0) {
        throw new Error(`Node hex string at index ${i} is empty`);
      }

      // Check if this might be a raw transaction hex instead of TreeNode hex
      // Raw transaction hex typically starts with version (03000000 for version 3, required for TRUC/ephemeral anchors)
      if (
        hex.startsWith("03000000") ||
        hex.startsWith("02000000") ||
        hex.startsWith("01000000")
      ) {
        throw new Error(
          `Node hex string at index ${i} appears to be a raw transaction hex, not a TreeNode protobuf. Use 'leafidtohex' command to convert node IDs to proper hex strings.`,
        );
      }

      const nodeBytes = hexToBytes(hex);
      const node = TreeNode.decode(nodeBytes);

      // Validate that the decoded node has required fields
      if (!node.id) {
        throw new Error(
          `Decoded TreeNode at index ${i} is missing required 'id' field`,
        );
      }
      if (!node.nodeTx || node.nodeTx.length === 0) {
        throw new Error(
          `Decoded TreeNode at index ${i} is missing required 'nodeTx' field`,
        );
      }

      nodes.push(node);
    } catch (decodeError) {
      throw new Error(
        `Failed to decode TreeNode hex string at index ${i}: ${decodeError}. Make sure you're providing TreeNode protobuf hex strings, not raw transaction hex. Use 'leafidtohex' command to get proper hex strings.`,
      );
    }
  }

  // Create a map of nodes by ID for easy lookup
  const nodeMap = new Map<string, TreeNode>();
  for (const node of nodes) {
    nodeMap.set(node.id, node);
  }

  // Create a map of transactions that have already been broadcast to prevent
  // attempting to do so again.
  const broadcastTxs = new Map<string, boolean>();

  // For each provided node, build its complete chain to the root
  for (const node of nodes) {
    const txPackages: FeeBumpTxPackage[] = [];
    let previousFeeBumpTx: string | undefined;

    // Build the chain from this node to the root
    // TODO(aakselrod): check whether
    // - two leaves are hanging off the same tree
    // - any ancestor nodes are already broadcast/confirmed
    const chain: TreeNode[] = [];
    let currentNode = node;

    // Walk up the chain to find the root, querying for each parent
    while (currentNode) {
      chain.unshift(currentNode);

      if (currentNode.parentNodeId) {
        // Check if we already have the parent in our map (from previous queries)
        let parentNode = nodeMap.get(currentNode.parentNodeId);

        if (!parentNode && sparkClient) {
          // Query for the parent node
          try {
            const response = await sparkClient.query_nodes({
              source: {
                $case: "nodeIds",
                nodeIds: {
                  nodeIds: [currentNode.parentNodeId],
                },
              },
              includeParents: true,
              network: network || 0, // Default to mainnet if not provided
            });

            parentNode = response.nodes[currentNode.parentNodeId];

            if (parentNode) {
              // Add to our map for future use
              nodeMap.set(currentNode.parentNodeId, parentNode);
            }
          } catch (error) {
            console.warn(
              `Failed to query parent node ${currentNode.parentNodeId}: ${error}`,
            );
            break;
          }
        }

        if (parentNode) {
          currentNode = parentNode;
        } else {
          if (!sparkClient) {
            console.warn(
              `Parent node ${currentNode.parentNodeId} not found. Provide a sparkClient to fetch missing parents.`,
            );
          } else {
            console.warn(
              `Parent node ${currentNode.parentNodeId} not found in database. Chain may be incomplete.`,
            );
          }
          break;
        }
      } else {
        // We've reached the root
        break;
      }
    }

    // Now walk down the chain from root to leaf to build fee bump packages
    for (const chainNode of chain) {
      // Add node tx and its fee bump
      let nodeTxHex = bytesToHex(chainNode.nodeTx);

      // Robust check for malformed ephemeral anchor in nodeTx
      try {
        const txObj = getTxFromRawTxHex(nodeTxHex);
        const txid = getTxId(txObj);
        if (broadcastTxs.get(txid)) {
          // We already created a package for this node in another leaf.
          continue;
        }
        broadcastTxs.set(txid, true);
        const isBroadcast = await isTxBroadcast(txid, electrsUrl, network);
        if (isBroadcast) {
          // This node has already been broadcast, so we don't need to do so.
          continue;
        } else {
        }
        let anchorOutputScriptHex: string | undefined;
        for (let i = txObj.outputsLength - 1; i >= 0; i--) {
          const output = txObj.getOutput(i);
          if (output?.amount === 0n && output.script) {
            anchorOutputScriptHex = bytesToHex(output.script);
            break;
          }
        }
      } catch (parseError) {
        console.error(
          `❌ Error parsing nodeTx for anchor check (node ${chainNode.id}): ${parseError}`,
        );
        console.log(
          `    This may indicate a corrupted transaction in the TreeNode.`,
        );
        console.log(`    Transaction hex: ${nodeTxHex}`);

        // Try to proceed anyway, but warn the user
        console.log(
          `    Attempting to continue with original hex, but fee bump may fail.`,
        );
      }

      const {
        feeBumpPsbt: nodeFeeBumpPsbt,
        usedUtxos,
        correctedParentTx,
      } = constructFeeBumpTx(nodeTxHex, availableUtxos, feeRate, undefined);

      const feeBumpTx = btc.Transaction.fromPSBT(hexToBytes(nodeFeeBumpPsbt));

      // Get the fee bump transaction's output, if any
      var feeBumpOut: psbt.TransactionOutput | null =
        feeBumpTx.outputsLength === 1 ? feeBumpTx.getOutput(0) : null;
      var feeBumpOutPubKey: string | null = null;

      // Remove used UTXOs from the available list
      for (const usedUtxo of usedUtxos) {
        if (feeBumpOut && bytesToHex(feeBumpOut.script!) == usedUtxo.script) {
          feeBumpOutPubKey = usedUtxo.publicKey;
        }
        const index = availableUtxos.findIndex(
          (u) => u.txid === usedUtxo.txid && u.vout === usedUtxo.vout,
        );
        if (index !== -1) {
          availableUtxos.splice(index, 1);
        }
      }

      // If the fee bump TX has an output, it should have the same key as the
      // input. We can add the output to the beginning of the list.
      if (feeBumpOut)
        availableUtxos.unshift({
          txid: getTxId(feeBumpTx),
          vout: 0,
          value: feeBumpOut.amount!,
          script: bytesToHex(feeBumpOut.script!),
          publicKey: feeBumpOutPubKey!,
        });

      // Use the corrected parent transaction if it was fixed
      const finalNodeTx = correctedParentTx || nodeTxHex;
      txPackages.push({ tx: finalNodeTx, feeBumpPsbt: nodeFeeBumpPsbt });

      // If this is the original node we started with, also add its refund tx
      if (chainNode.id === node.id) {
        let refundTxHex = bytesToHex(chainNode.refundTx);

        // Robust check for malformed ephemeral anchor in refundTx
        try {
          const txObj = getTxFromRawTxHex(refundTxHex);
          let anchorOutputScriptHex: string | undefined;
          for (let i = txObj.outputsLength - 1; i >= 0; i--) {
            const output = txObj.getOutput(i);
            if (output?.amount === 0n && output.script) {
              anchorOutputScriptHex = bytesToHex(output.script);
              break;
            }
          }
        } catch (parseError) {
          console.error(
            `❌ Error parsing refundTx for anchor check (node ${chainNode.id}): ${parseError}`,
          );
          console.log(
            `    This may indicate a corrupted refund transaction in the TreeNode.`,
          );
          console.log(`    Refund transaction hex: ${refundTxHex}`);

          // Try to proceed anyway, but warn the user
          console.log(
            `    Attempting to continue with original refund hex, but this transaction may be invalid.`,
          );
        }

        const refundFeeBump = constructFeeBumpTx(
          refundTxHex,
          availableUtxos,
          feeRate,
          undefined,
        );

        const feeBumpTx = btc.Transaction.fromPSBT(
          hexToBytes(refundFeeBump.feeBumpPsbt),
        );

        var feeBumpOut: psbt.TransactionOutput | null =
          feeBumpTx.outputsLength === 1 ? feeBumpTx.getOutput(0) : null;
        var feeBumpOutPubKey: string | null = null;

        // Remove used UTXOs from the available list
        for (const usedUtxo of usedUtxos) {
          if (feeBumpOut && bytesToHex(feeBumpOut.script!) == usedUtxo.script) {
            feeBumpOutPubKey = usedUtxo.publicKey;
          }
          const index = availableUtxos.findIndex(
            (u) => u.txid === usedUtxo.txid && u.vout === usedUtxo.vout,
          );
          if (index !== -1) {
            availableUtxos.splice(index, 1);
          }
        }

        if (feeBumpOut)
          // Add to end instead of the beginning in case there are other available UTXOs we can use first
          // We don't want to wait on leaf 1 to be broadcasted to broadcast leaf 2 if we can use a different available UTXO
          availableUtxos.push({
            txid: getTxId(feeBumpTx),
            vout: 0,
            value: feeBumpOut.amount!,
            script: bytesToHex(feeBumpOut.script!),
            publicKey: feeBumpOutPubKey!,
          });

        txPackages.push({
          tx: refundTxHex,
          feeBumpPsbt: refundFeeBump.feeBumpPsbt,
        });
      }
    }

    result.push({
      leafId: node.id,
      txPackages,
    });
  }

  return result;
}

// Helper function to create RIPEMD160(SHA256(data)) hash
function hash160(data: Uint8Array): Uint8Array {
  // Proper implementation using RIPEMD160(SHA256(data))
  const sha256Hash = sha256(data);
  return ripemd160(sha256Hash);
}

// Helper function to construct a fee bump tx for a given tx using available UTXOs
export function constructFeeBumpTx(
  txHex: string,
  utxos: Utxo[],
  feeRate: FeeRate,
  previousFeeBumpTx?: string, // Optional previous fee bump tx to chain from
): { feeBumpPsbt: string; usedUtxos: Utxo[]; correctedParentTx?: string } {
  // Validate inputs first
  if (!txHex || txHex.length === 0) {
    throw new Error("Transaction hex string is empty or undefined");
  }

  if (utxos.length === 0) {
    throw new Error("No UTXOs available for fee bump");
  }

  // Check for and fix malformed ephemeral anchor BEFORE parsing
  let correctedTxHex = txHex;

  // Decode the parent tx using the utility function with error handling
  let parentTx: any;
  try {
    parentTx = getTxFromRawTxHex(correctedTxHex);
    if (!parentTx) {
      throw new Error("getTxFromRawTxHex returned null/undefined");
    }
  } catch (parseError) {
    throw new Error(
      `Failed to parse parent transaction hex: ${parseError}. Transaction hex: ${correctedTxHex}`,
    );
  }

  // Validate the parsed transaction has the expected structure
  try {
    const outputsLength = parentTx.outputsLength;
    const inputsLength = parentTx.inputsLength;
    if (typeof outputsLength !== "number" || outputsLength < 0) {
      throw new Error(
        "Invalid transaction: outputsLength is not a valid number",
      );
    }
    if (typeof inputsLength !== "number" || inputsLength < 0) {
      throw new Error(
        "Invalid transaction: inputsLength is not a valid number",
      );
    }
  } catch (validationError) {
    throw new Error(
      `Transaction validation failed: ${validationError}. This may indicate a corrupted or malformed transaction.`,
    );
  }

  // Find the ephemeral anchor output (should be the last output with 0 value and OP_TRUE script)
  const parentTxIdFromLib = parentTx.id; // Use the library's built-in ID property

  let ephemeralAnchorIndex = -1;

  for (let i = 0; i < parentTx.outputsLength; i++) {
    const output = parentTx.getOutput(i);

    // Check for ephemeral anchor: 0 value and OP_TRUE script patterns
    const isEphemeralAnchor = isEphemeralAnchorOutput(
      output?.script,
      output?.amount,
    );

    if (isEphemeralAnchor) {
      ephemeralAnchorIndex = i;
      break;
    }
  }
  if (ephemeralAnchorIndex === -1) {
    throw new Error(
      "No ephemeral anchor output found in parent transaction. Expected a 0-value output with OP_TRUE script (0x51), malformed OP_TRUE (0x0151), Bitcoin v29 ephemeral anchor script (015152014e0173), or Bitcoin OP_1 + push 2 bytes script (51024e73).",
    );
  }

  const ephemeralAnchorOutput = parentTx.getOutput(ephemeralAnchorIndex);
  if (!ephemeralAnchorOutput)
    throw new Error("No ephemeral anchor output found");
  if (!ephemeralAnchorOutput.script)
    throw new Error("No script found in ephemeral anchor output");

  // Use all available UTXOs for funding
  if (utxos.length === 0) {
    throw new Error("No UTXOs available for fee bump");
  }

  // Create a new transaction using the builder pattern
  const builder = new btc.Transaction({
    version: 3,
    allowUnknown: true,
    allowLegacyWitnessUtxo: true,
  }); // ✅ set v3 in constructor

  // Track total value and process each funding UTXO
  let totalValue = 0n;
  const processedUtxos: Array<{
    utxo: Utxo;
    p2wpkhScript: Uint8Array;
  }> = [];

  for (let i = 0; i < utxos.length; i++) {
    const fundingUtxo = utxos[i];
    if (!fundingUtxo) {
      throw new Error(`UTXO at index ${i} is undefined`);
    }

    // Derive the public key from the private key and create the correct script
    const pubKeyHash = hash160(hexToBytes(fundingUtxo.publicKey));

    const scriptToUse = new Uint8Array([0x00, 0x14, ...pubKeyHash]); // OP_0 + 20-byte hash (P2WPKH)

    // Check if provided script is P2PKH and warn user
    const providedScript = hexToBytes(fundingUtxo.script);
    if (bytesToHex(scriptToUse) !== bytesToHex(providedScript)) {
      throw new Error(
        `❌  Derived script doesn't match provided script for UTXO ${i + 1}.`,
      );
    }

    // Add funding UTXO as segwit input using witnessUtxo (P2WPKH only)
    builder.addInput({
      txid: fundingUtxo.txid,
      index: fundingUtxo.vout,
      sequence: 0xffffffff,
      witnessUtxo: {
        script: scriptToUse, // Always P2WPKH
        amount: fundingUtxo.value,
      },
    });

    totalValue += fundingUtxo.value;
    processedUtxos.push({
      utxo: fundingUtxo,
      p2wpkhScript: scriptToUse,
    });
  }

  // Add ephemeral anchor output as the last input - use direct script spend (not P2WSH)
  builder.addInput({
    txid: parentTxIdFromLib,
    index: ephemeralAnchorIndex,
    sequence: 0xffffffff,
    witnessUtxo: {
      script: ephemeralAnchorOutput.script, // Use the original script directly (not P2WSH wrapped)
      amount: 0n,
    },
  });

  // Use fixed fee of 1500 satoshis
  // TODO(aakelrod): fix this
  const fee = 1500n;

  // Minimum change amount (546 satoshis for a standard output)
  const remainingValue = totalValue - fee;

  if (remainingValue <= 0n) {
    throw new Error(
      `Insufficient funds for fee bump. Required fee: ${fee} sats, Available: ${totalValue} sats`,
    );
  }

  // Add output with remaining value using the first UTXO's P2WPKH script
  if (processedUtxos.length === 0) {
    throw new Error("No processed UTXOs available for change output");
  }

  const firstProcessedUtxo = processedUtxos[0];
  if (!firstProcessedUtxo) {
    throw new Error("First processed UTXO is undefined");
  }

  builder.addOutput({
    script: firstProcessedUtxo.p2wpkhScript,
    amount: remainingValue,
  });

  // Sign all funding UTXO inputs on the builder
  for (let i = 0; i < processedUtxos.length; i++) {
    const processed = processedUtxos[i];
    if (!processed) {
      throw new Error(`Processed UTXO at index ${i} is undefined`);
    }

    try {
      // Set proper witness script for P2WPKH input
      builder.updateInput(i, {
        witnessScript: processed.p2wpkhScript,
      });
      builder.signIdx;
    } catch (error) {
      throw new Error(`Failed to handle funding UTXO input ${i + 1}: ${error}`);
    }
  }

  // Extract transaction bytes (funding inputs are finalized, ephemeral anchor has empty witness)
  let psbtHex;
  try {
    psbtHex = bytesToHex(builder.toPSBT());
  } catch (error) {
    throw new Error(`Failed to extract transaction: ${error}`);
  }

  // Return both the fee bump transaction hex and the UTXOs used
  return {
    feeBumpPsbt: psbtHex,
    usedUtxos: utxos,
    correctedParentTx: correctedTxHex !== txHex ? correctedTxHex : undefined,
  };
}
