import { networks } from "bitcoinjs-lib";
import { TokenPubkey, Lrc20TransactionDto } from "../lrc/types";
import { LRCWallet } from "../lrc/wallet";
import { NetworkType } from "../network";
import { JSONStringify } from "../lrc/utils";

const REVOCATION_ADDRESS = "tb1ppfwjeshkreeq9kl7tmj7wa638pj2kck0kc8zkq94r238nasejegqs6ptv3";
const DELAY_ADDRESS = "tb1ppfwjeshkreeq9kl7tmj7wa638pj2kck0kc8zkq94r238nasejegqs6ptv3";
const LOCKTIME = 150;
const TOKEN_PUBKEY = new TokenPubkey(
  Buffer.from("e85316cc097bd7dffbc97c2ceeeb2ff984eccb227cdac6b29bad0b1e02146c0d", "hex")
);
const SATOSHIS = 15000;

const wallet = new LRCWallet(
  "4799979d5e417e3d6d00cf89a77d4f3c0354d295810326c6b0bf4b45aedb38f3",
  networks.testnet,
  NetworkType.REGTEST
);

const main = async () => {
  await wallet.syncWallet();

  const payment = {
    amount: BigInt(1000),
    tokenPubkey: "tb1ppfwjeshkreeq9kl7tmj7wa638pj2kck0kc8zkq94r238nasejegqs6ptv3",
    sats: SATOSHIS,
    cltvOutputLocktime: LOCKTIME,
    recipient: REVOCATION_ADDRESS,
    expiryKey: DELAY_ADDRESS,
    metadata: { txHash: "63e7487c274aa618552071b468bb7f9ef2c34fda93de28b49fa9b9baf1b2f1a9", index: 2 },
  };

  let exitTx = await wallet.prepareSparkExit([payment], 1.0);

  let txDto = Lrc20TransactionDto.fromLrc20Transaction(exitTx);
  console.log(JSONStringify(txDto));
};

main();
