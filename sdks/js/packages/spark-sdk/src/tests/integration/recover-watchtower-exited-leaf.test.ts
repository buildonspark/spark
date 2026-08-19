import { bytesToHex } from "@noble/curves/utils";
import { type ConfigOptions } from "../../services/wallet-config.js";
import { type TreeNode } from "../../proto/spark.js";
import {
  getCurrentTimelock,
  getTxFromRawTxBytes,
  getTxFromRawTxHex,
  getTxId,
} from "../../utils/index.js";
import { SparkWalletTestingIntegrationWithStream } from "../utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "../utils/test-faucet.js";
import { waitForClaim } from "../utils/utils.js";

// Leaf optimization would split or merge the single leaf this test follows
// across its renewal chain, so it stays off on both wallets.
const options: ConfigOptions = {
  network: "LOCAL",
  optimizationOptions: {
    auto: false,
    multiplicity: 0,
  },
};

const DEPOSIT_SATS = 100_000n;
const TRANSFER_AMOUNT = 100_000;
const INITIAL_TIMELOCK = 2000;
// Each renewal appends a split node above the leaf. Two of them give us a
// grandparent to confirm and a parent for the watchtower to exit, which is the
// shape that strands the leaf.
const RENEWALS_REQUIRED = 2;
// 18 transfers walk the refund timelock 2000 -> 200, the 19th renews. Twice
// that, plus slack for the tail.
const MAX_TRANSFERS = 60;

type Wallet = SparkWalletTestingIntegrationWithStream;

async function sparkClientFor(wallet: Wallet) {
  return await wallet
    .getConnectionManager()
    .createSparkClient(wallet.getConfigService().getCoordinatorAddress());
}

/**
 * Reads a node straight from the coordinator. getLeaves() only returns AVAILABLE
 * leaves, and everything this test asserts happens after the leaf leaves that
 * pool.
 */
async function queryNode(
  wallet: Wallet,
  nodeId: string,
  includeParents = false,
): Promise<Record<string, TreeNode>> {
  const client = await sparkClientFor(wallet);
  const response = await client.query_nodes({
    source: { $case: "nodeIds", nodeIds: { nodeIds: [nodeId] } },
    includeParents,
    network: wallet.getConfigService().getNetworkProto(),
  });
  return response.nodes;
}

async function nodeStatus(wallet: Wallet, nodeId: string): Promise<string> {
  const nodes = await queryNode(wallet, nodeId);
  return nodes[nodeId]?.status ?? "MISSING";
}

function refundTimelockOf(leaf: TreeNode): number {
  const tx = getTxFromRawTxBytes(leaf.refundTx);
  return getCurrentTimelock(tx.getInput(0).sequence ?? 0);
}

async function transferWholeLeaf(from: Wallet, to: Wallet): Promise<void> {
  await from.transfer({
    amountSats: TRANSFER_AMOUNT,
    receiverSparkAddress: await to.getSparkAddress(),
  });
  await waitForClaim({ wallet: to });
  await from.syncWalletForTesting();
  await to.syncWalletForTesting();
}

async function onlyLeaf(wallet: Wallet): Promise<TreeNode> {
  const leaves = await wallet.getLeaves();
  expect(leaves.length).toBe(1);
  return leaves[0]!;
}

async function waitForStatus(
  wallet: Wallet,
  nodeId: string,
  expected: string,
  faucet: BitcoinFaucet,
  { attempts = 30, blocksPerAttempt = 2 } = {},
): Promise<void> {
  for (let i = 0; i < attempts; i++) {
    if ((await nodeStatus(wallet, nodeId)) === expected) return;
    // The chain watcher only acts on a new block, so drive it rather than
    // sleeping.
    await faucet.mineBlocksAndWaitForMiningToComplete(blocksPerAttempt);
  }
  throw new Error(
    `node ${nodeId} never reached ${expected}, last saw ${await nodeStatus(wallet, nodeId)}`,
  );
}

/** The outpoint a signed recovery transaction spends. */
function spentOutpoint(txHex: string): string {
  const input = getTxFromRawTxHex(txHex).getInput(0);
  return `${bytesToHex(input.txid!)}:${input.index}`;
}

/**
 * Stranding a leaf costs dozens of transfers, so the cases below share one and
 * run in order rather than each building their own. Recovery is repeatable by
 * design — every attempt spends the same output and at most one can confirm —
 * so a later case re-signing after an earlier one is the intended usage, not an
 * artefact of the sharing.
 */
describe("Recover a watchtower-exited leaf", () => {
  const faucet = BitcoinFaucet.getInstance();
  let holder: Wallet;
  let other: Wallet;
  let leafId: string;
  let parentDirectTxid: string;
  let destinationAddress: string;
  let explicitRecoveryTxHex: string;

  beforeAll(async () => {
    const { wallet: walletA } =
      await SparkWalletTestingIntegrationWithStream.initialize({ options });
    const { wallet: walletB } =
      await SparkWalletTestingIntegrationWithStream.initialize({ options });

    // ---- Fund wallet A with a single-use deposit -------------------------
    const depositAddress = await walletA.getSingleUseDepositAddress();
    expect(depositAddress).toBeDefined();

    const depositTx = await faucet.sendToAddress(depositAddress, DEPOSIT_SATS);
    await faucet.mineBlocksAndWaitForMiningToComplete(6);
    await walletA.claimDeposit(depositTx.id);
    await waitForClaim({ wallet: walletA });

    let leaf = await onlyLeaf(walletA);
    expect(refundTimelockOf(leaf)).toBe(INITIAL_TIMELOCK);
    leafId = leaf.id;

    // ---- Transfer back and forth until the leaf renews twice -------------
    // A deposit-root leaf's node tx has a final sequence, so every renewal
    // takes the zero-timelock path and appends a split node.
    holder = walletA;
    other = walletB;
    let renewals = 0;
    let previousTimelock = refundTimelockOf(leaf);

    for (let i = 0; renewals < RENEWALS_REQUIRED && i < MAX_TRANSFERS; i++) {
      await transferWholeLeaf(holder, other);
      [holder, other] = [other, holder];

      leaf = await onlyLeaf(holder);
      const timelock = refundTimelockOf(leaf);
      // A renewal is the only thing that raises the refund timelock.
      if (timelock > previousTimelock) renewals++;
      previousTimelock = timelock;
    }
    expect(renewals).toBe(RENEWALS_REQUIRED);
    // The leaf keeps its identity across renewals; only its ancestry grows.
    expect(leaf.id).toBe(leafId);

    // ---- Identify the two split nodes above the leaf ---------------------
    const withParents = await queryNode(holder, leafId, true);
    const parentId = withParents[leafId]?.parentNodeId;
    expect(parentId).toBeDefined();
    const parent = withParents[parentId!];
    expect(parent).toBeDefined();
    const grandparentId = parent!.parentNodeId;
    expect(grandparentId).toBeDefined();
    const grandparent = withParents[grandparentId!];
    expect(grandparent).toBeDefined();

    // ---- Confirm the grandparent, so the watchtower can act on the parent --
    // Confirming the grandparent's node tx is what tells the watchtower an exit
    // is underway below it. It is a zero-fee CPFP transaction, so it has to be
    // mined directly rather than relayed.
    const grandparentTxHex = getTxFromRawTxBytes(grandparent!.nodeTx).hex;
    await faucet.generateBlockWithTxs([grandparentTxHex]);
    await faucet.mineBlocksAndWaitForMiningToComplete(6);

    // ---- Let the watchtower publish the parent's direct tx ---------------
    // Its relative timelock is DirectTimelockOffset (50) blocks past the
    // grandparent's confirmation, and once it confirms it conflict-spends the
    // outpoint the leaf's own transactions name — which is what strands the
    // leaf.
    await faucet.mineBlocksAndWaitForMiningToComplete(55);
    await waitForStatus(holder, leafId, "WATCHTOWER_EXITED", faucet);

    parentDirectTxid = getTxId(getTxFromRawTxBytes(parent!.directTx));
    destinationAddress = await faucet.getNewAddress();
  }, 900_000);

  it("lists the stranded leaf with the output that can still be spent", async () => {
    // Nothing else surfaces this leaf: it stopped being AVAILABLE the moment
    // the watchtower's transaction confirmed, so it is absent from the balance
    // and from getLeaves.
    expect(await holder.getLeaves()).toEqual([]);

    const stranded = await holder.getWatchtowerExitedLeaves();

    expect(stranded.length).toBe(1);
    const [entry] = stranded;
    expect(entry!.leafId).toBe(leafId);
    expect(entry!.status).toBe("WATCHTOWER_EXITED");
    expect(entry!.valueSats).toBe(TRANSFER_AMOUNT);
    // Resolved from ancestor status alone, with no txid supplied — the renewal
    // chain holds several transactions paying this same key.
    expect(entry!.recovery?.txid).toBe(parentDirectTxid);
    expect(entry!.recovery?.valueSats).toBeGreaterThan(0);
  }, 120_000);

  it("co-signs the output the listing named, and retires the leaf", async () => {
    // Fed from the listing rather than from parentDirectTxid, so the two halves
    // of the API are exercised as a caller would use them: whatever
    // getWatchtowerExitedLeaves resolved is what gets spent, output index
    // included.
    const [stranded] = await holder.getWatchtowerExitedLeaves();
    const recovery = stranded!.recovery!;

    explicitRecoveryTxHex = await holder.recoverWatchtowerExitedLeaf({
      leafId: stranded!.leafId,
      recoveryTxid: recovery.txid,
      outputIndex: recovery.outputIndex,
      destinationAddress,
      satsPerVbyteFee: 5,
    });
    expect(spentOutpoint(explicitRecoveryTxHex)).toBe(
      `${recovery.txid}:${recovery.outputIndex}`,
    );
    expect(
      Number(getTxFromRawTxHex(explicitRecoveryTxHex).getOutput(0).amount),
    ).toBeLessThan(recovery.valueSats);

    // The signature is what retires the leaf, not the broadcast.
    expect(await nodeStatus(holder, leafId)).toBe("WATCHTOWER_EXIT_RECOVERED");

    const [recovered] = await holder.getWatchtowerExitedLeaves();
    expect(recovered?.status).toBe("WATCHTOWER_EXIT_RECOVERED");
  }, 120_000);

  it("re-signs at a higher fee without a recoveryTxid, spending the same output", async () => {
    const bumpedRecoveryTxHex = await holder.recoverWatchtowerExitedLeaf({
      leafId,
      destinationAddress,
      satsPerVbyteFee: 20,
    });

    // Picking the source transaction unaided has to land on the same outpoint,
    // or the two recoveries would not be alternatives to each other.
    expect(spentOutpoint(bumpedRecoveryTxHex)).toBe(
      spentOutpoint(explicitRecoveryTxHex),
    );
    const bumped = getTxFromRawTxHex(bumpedRecoveryTxHex).getOutput(0).amount!;
    const original = getTxFromRawTxHex(explicitRecoveryTxHex).getOutput(
      0,
    ).amount!;
    expect(bumped).toBeLessThan(original);

    const recoveryTxid = await faucet.broadcastTx(bumpedRecoveryTxHex);
    expect(recoveryTxid).toBeTruthy();
    await faucet.mineBlocksAndWaitForMiningToComplete(2);
  }, 120_000);

  // That a recovered leaf can no longer move off-chain is deliberately not
  // asserted here: the wallet has no spendable leaf left either way, so a
  // transfer would be refused for want of funds whatever the status rule said.
  // The WATCHTOWER_EXIT_RECOVERED assertions above are the real evidence, and the
  // rule itself is covered by the transfer handler's own tests.
});
