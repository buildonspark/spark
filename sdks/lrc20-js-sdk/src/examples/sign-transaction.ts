import { networks } from "bitcoinjs-lib";
import { LRCWallet } from "../lrc/wallet";
import { NetworkType } from "../network";

let wallet = new LRCWallet(
  "4799979d5e417e3d6d00cf89a77d4f3c0354d295810326c6b0bf4b45aedb38f3",
  networks.testnet,
  NetworkType.TESTNET
);

async function main() {
  const rawTxHex =
    "010000000118d4cdfaa0fe22ad254f9d83aa5cad7775d8d3669f9d11dc0394ee05bb48f1a70100000000fdffffff021027000000000000160014937f861346b461bbd2d12e59cec3f362462d05f69c5d0100000000001600142c528b8e9c42ca0c94901b6451811b7456377b6100000000";

  const signedTx = await wallet.signRawTransaction(rawTxHex);

  const result = await wallet.broadcastRawBtcTransaction(signedTx.toHex());

  console.log(result);
}

main();
