import { networks } from "bitcoinjs-lib";
import { Lrc20TransactionDto } from "../lrc/types";
import { LRCWallet } from "../lrc/wallet";
import { NetworkType } from "../network";

const REVOCATION_KEY = "02e85316cc097bd7dffbc97c2ceeeb2ff984eccb227cdac6b29bad0b1e02146c0d";
const DELAY_KEY = "02e85316cc097bd7dffbc97c2ceeeb2ff984eccb227cdac6b29bad0b1e02146c0d";
const LOCKTIME = 150;
const TOKEN_PUBKEY = "02e85316cc097bd7dffbc97c2ceeeb2ff984eccb227cdac6b29bad0b1e02146c0d";
const SATOSHIS = 15000;

const wallet = new LRCWallet(
  "4799979d5e417e3d6d00cf89a77d4f3c0354d295810326c6b0bf4b45aedb38f3",
  networks.regtest,
  NetworkType.REGTEST
);

const main = async () => {
  await wallet.syncWallet();

  const payment = {
    amount: BigInt(1000),
    tokenPubkey: TOKEN_PUBKEY,
    sats: SATOSHIS,
    cltvOutputLocktime: LOCKTIME,
    revocationKey: REVOCATION_KEY,
    expiryKey: DELAY_KEY,
    metadata: { token_tx_hash: "63e7487c274aa618552071b468bb7f9ef2c34fda93de28b49fa9b9baf1b2f1a9", exit_leaf_index: 2 },
  };

  const exitTx = await wallet.prepareSparkExit([payment], 1.0);

  const txDto = Lrc20TransactionDto.fromLrc20Transaction(exitTx);

  const result = await wallet.broadcast(txDto);

  console.log(result);
};

main();
