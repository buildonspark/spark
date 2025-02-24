import { bytesToHex, bytesToNumberBE } from "@noble/curves/abstract/utils";
import { hashTokenTransaction } from "./token-hashing.js";
export function getTokenLeavesSum(leaves) {
    return leaves.reduce((sum, leaf) => sum + BigInt(bytesToNumberBE(leaf.leaf.tokenAmount)), BigInt(0));
}
export function extractOutputLeaves(fullTokenTransaction) {
    const outputLeaves = [];
    const hash = hashTokenTransaction(fullTokenTransaction, true);
    fullTokenTransaction.outputLeaves.forEach((output, index) => {
        outputLeaves.push({
            leaf: output,
            previousTransactionHash: hash,
            previousTransactionVout: index,
        });
    });
    return outputLeaves;
}
export function calculateAvailableTokenAmount(outputLeaves) {
    return outputLeaves.reduce((sum, leaf) => sum + BigInt(bytesToNumberBE(leaf.leaf.tokenAmount)), BigInt(0));
}
export function checkIfSelectedLeavesAreAvailable(selectedLeaves, tokenLeaves, tokenPublicKey) {
    const tokenPubKeyHex = bytesToHex(tokenPublicKey);
    const tokenLeavesAvailable = tokenLeaves.get(tokenPubKeyHex);
    if (!tokenLeavesAvailable) {
        return false;
    }
    if (selectedLeaves.length === 0 ||
        tokenLeavesAvailable.length < selectedLeaves.length) {
        return false;
    }
    // Create a Set of available leaf IDs for O(n + m) lookup
    const availableLeafIds = new Set(tokenLeavesAvailable.map((leaf) => leaf.leaf.id));
    for (const selectedLeaf of selectedLeaves) {
        if (!selectedLeaf.leaf?.id || !availableLeafIds.has(selectedLeaf.leaf.id)) {
            return false;
        }
    }
    return true;
}
//# sourceMappingURL=token-transactions.js.map