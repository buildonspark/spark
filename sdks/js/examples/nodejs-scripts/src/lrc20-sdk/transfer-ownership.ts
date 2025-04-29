import { address, networks } from "bitcoinjs-lib";
import { LRCWallet } from "@buildonspark/lrc20-sdk";
import { NetworkType } from "@buildonspark/lrc20-sdk";
import {
  TokenPubkey,
  TransferOwnershipAnnouncement,
  Lrc20TransactionDto,
} from "@buildonspark/lrc20-sdk";
import { JSONStringify } from "@buildonspark/lrc20-sdk";

let wallet = new LRCWallet(
  "917a8999adaad1685e1fa5b50283ada23f910481da8e51867cdd7a9329874733",
  networks.testnet,
  NetworkType.TESTNET,
);

async function main() {
  await wallet.syncWallet();

  let newOwner = address.fromBech32(
    "tb1ppfwjeshkreeq9kl7tmj7wa638pj2kck0kc8zkq94r238nasejegqs6ptv3",
  );
  let transferOwnership = new TransferOwnershipAnnouncement(
    new TokenPubkey(
      Buffer.from(
        "6c0c308af25ace8219cdf44c7981edd5908e9f1f9f4e4155e86828957a432df6",
        "hex",
      ),
    ),
    newOwner.data,
  );

  let transferOwnershipTx = await wallet.prepareTransferOwnership(
    transferOwnership,
    1.0,
  );

  let txDto = Lrc20TransactionDto.fromLrc20Transaction(transferOwnershipTx);
  console.log(JSONStringify(txDto));

  let res = await wallet.broadcast(txDto);

  console.log(res);
}

main();
