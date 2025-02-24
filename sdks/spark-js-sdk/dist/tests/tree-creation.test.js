import { describe, expect, it } from "@jest/globals";
import { bytesToHex } from "@noble/curves/abstract/utils";
import { getTxFromRawTxBytes, getTxId } from "../utils/bitcoin.js";
import { Network } from "../utils/network.js";
import { createDummyTx } from "../utils/wasm.js";
import { SparkWalletTesting } from "./utils/spark-testing-wallet.js";
describe("Tree Creation", () => {
    // Skip all tests if running in GitHub Actions
    const testFn = process.env.GITHUB_ACTIONS ? it.skip : it;
    testFn("test tree creation address generation", async () => {
        const wallet = new SparkWalletTesting(Network.LOCAL);
        await wallet.initWalletFromMnemonic();
        const pubKey = await wallet.getSigner().generatePublicKey();
        const depositResp = await wallet.generateDepositAddress(pubKey);
        expect(depositResp.depositAddress).toBeDefined();
        const dummyTx = createDummyTx({
            address: depositResp.depositAddress.address,
            amountSats: 65536n,
        });
        const depositTxHex = bytesToHex(dummyTx.tx);
        const depositTx = getTxFromRawTxBytes(dummyTx.tx);
        const vout = 0;
        const txid = getTxId(depositTx);
        if (!txid) {
            throw new Error("txid not found");
        }
        const treeResp = await wallet.generateDepositAddressForTree(vout, pubKey, depositTx);
        const treeNodes = await wallet.createTree(vout, treeResp, true, depositTx);
        console.log("tree nodes:", treeNodes);
    }, 30000);
});
//# sourceMappingURL=tree-creation.test.js.map