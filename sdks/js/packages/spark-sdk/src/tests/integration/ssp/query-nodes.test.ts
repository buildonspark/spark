import { describe, expect, it } from "@jest/globals";
import { type TreeNode } from "../../../proto/spark.js";
import { Network, NetworkToProto } from "../../../utils/network.js";
import { SparkWalletTestingIntegrationWithStream } from "../../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../../utils/test-faucet.js";
import { retryUntilSuccess, waitForBalance } from "../../utils/utils.js";

// Two separate claims are what give the wallet more than one leaf. Asking a
// single claim for a non-power-of-two credit would also split into several
// leaves, but only while the SSP pool happens to hold every denomination the
// split wants — and the regtest pool only drains over a CI run. These amounts
// match the sibling static-deposit tests: the SSP's flat regtest fee leaves a
// power-of-two credit, which one denomination can satisfy on its own.
const FIRST_DEPOSIT_AMOUNT = 2147n; // -> credit 2048
const SECOND_DEPOSIT_AMOUNT = 4195n; // -> credit 4096

async function claimStaticDeposit(
  wallet: SparkWalletTestingIntegrationWithStream,
  faucet: BitcoinFaucet,
  amount: bigint,
): Promise<number> {
  const depositAddress = await wallet.getStaticDepositAddress();
  const signedTx = await faucet.sendToAddress(depositAddress, amount);
  await faucet.mineBlocks(6);

  const quote = await wallet.getClaimStaticDepositQuote(signedTx.id);
  await retryUntilSuccess(
    async () =>
      await wallet.claimStaticDeposit({
        transactionId: signedTx.id,
        creditAmountSats: quote.creditAmountSats,
        sspSignature: quote.signature,
      }),
  );

  return quote.creditAmountSats;
}

function walkToRoot(
  leaf: TreeNode,
  nodesById: Map<string, TreeNode>,
): TreeNode[] {
  const chain: TreeNode[] = [leaf];

  let current = leaf;
  while (current.parentNodeId) {
    const parent = nodesById.get(current.parentNodeId);
    if (!parent) {
      throw new Error(
        `ancestor chain for leaf ${leaf.id} is broken: parent ${current.parentNodeId} of node ${current.id} was not returned by query_nodes`,
      );
    }
    if (chain.some((node) => node.id === parent.id)) {
      throw new Error(
        `ancestor chain for leaf ${leaf.id} cycles back to node ${parent.id}`,
      );
    }
    chain.push(parent);
    current = parent;
  }

  return chain;
}

describe("query_nodes ancestor chains", () => {
  it("returns a complete chain to the tree root for every claimed leaf", async () => {
    const faucet = BitcoinFaucet.getInstance();
    const { wallet } = await SparkWalletTestingIntegrationWithStream.initialize(
      {
        options: { network: "LOCAL" },
      },
    );

    try {
      // Claiming through the SSP is what makes this test meaningful: the leaves
      // come out of the SSP's own tree, which has real branch structure. A plain
      // single-use deposit builds a one-node tree, so the leaf would be its own
      // root and there would be no chain to walk.
      const firstCredit = await claimStaticDeposit(
        wallet,
        faucet,
        FIRST_DEPOSIT_AMOUNT,
      );
      const secondCredit = await claimStaticDeposit(
        wallet,
        faucet,
        SECOND_DEPOSIT_AMOUNT,
      );
      await waitForBalance(wallet, BigInt(firstCredit + secondCredit));

      const leaves = await wallet.getLeaves();
      expect(leaves.length).toBeGreaterThan(1);
      const leafIds = leaves.map((leaf) => leaf.id);

      const sparkClient = await wallet
        .getConnectionManager()
        .createSparkClient(wallet.getConfigService().getCoordinatorAddress());
      const queryNodes = (includeParents: boolean) =>
        sparkClient.query_nodes({
          source: { $case: "nodeIds", nodeIds: { nodeIds: leafIds } },
          includeParents,
          limit: 0,
          offset: 0,
          network: NetworkToProto[Network.LOCAL],
          statuses: [],
        });

      const withParents = await queryNodes(true);
      const nodesById = new Map(Object.entries(withParents.nodes));

      for (const leaf of leaves) {
        const returnedLeaf = nodesById.get(leaf.id);
        expect(returnedLeaf).toBeDefined();

        // Reaching a root is what walkToRoot returning at all proves: it only
        // stops on a node with no parent, and throws if any parent along the way
        // is missing from the response.
        const chain = walkToRoot(returnedLeaf!, nodesById);

        // A claimed leaf is served out of an SSP tree, so it is never its own root.
        expect(chain.length).toBeGreaterThan(1);
        for (const node of chain) {
          expect(node.treeId).toBe(leaf.treeId);
        }
      }

      // includeParents is what pulls the ancestry in, so without it the server
      // must return the requested leaves and nothing else.
      const leavesOnly = await queryNodes(false);
      expect(Object.keys(leavesOnly.nodes).sort()).toEqual([...leafIds].sort());
    } finally {
      await wallet.getConnectionManager().closeConnections();
    }
    // Generous so the waits below it can actually run to completion: two claims
    // at retryUntilSuccess's default 20x2s, plus a 30s waitForBalance, is ~110s
    // of budget. A shorter timeout would cut a legitimate retry short and report
    // a Jest timeout instead of the claim error that caused it.
  }, 180000);
});
