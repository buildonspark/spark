import { secp256k1 } from "@noble/curves/secp256k1";
import { Address, OutScript, Transaction } from "@scure/btc-signer";
import { RPCError } from "../errors/types.js";
import {
  DefaultSparkSigner,
  KeyDerivationType,
  UnsafeStatelessSparkSigner,
} from "../index.js";
import { FinalizeNodeSignaturesResponse, TreeNode } from "../proto/spark.js";
import { WalletConfigService } from "../services/config.js";
import { ConnectionManagerNodeJS } from "../services/connection/connection.node.js";
import { DepositService } from "../services/deposit.js";
import { ConfigOptions, WalletConfig } from "../services/wallet-config.js";
import { getP2TRAddressFromPublicKey } from "../utils/bitcoin.js";
import { getNetwork, Network } from "../utils/network.js";
import { SparkWalletTesting } from "./utils/spark-testing-wallet.js";
import { BitcoinFaucet } from "./utils/test-faucet.js";
import { sha256 } from "@noble/hashes/sha2";

export { BitcoinFaucet, SparkWalletTesting };

export function getTestWalletConfig() {
  const identityPrivateKey = secp256k1.utils.randomPrivateKey();
  return getTestWalletConfigWithIdentityKey(identityPrivateKey);
}

export function getTestWalletConfigWithIdentityKey(
  identityPrivateKey: Uint8Array,
) {
  return {
    ...WalletConfig.LOCAL,
    identityPrivateKey,
  } as ConfigOptions;
}

async function createDeposit(
  wallet: SparkWalletTesting,
  leafId: string,
  faucet: BitcoinFaucet,
  amountSats: bigint = 100_000n,
  includeDirectTx: boolean = true,
) {
  const faucetCoin = await faucet.fund();

  const configService = new WalletConfigService(
    {
      network: "LOCAL",
    },
    wallet.getSigner(),
  );
  const connectionManager = new ConnectionManagerNodeJS(configService);
  const depositService = new DepositService(configService, connectionManager);

  const pubKey = await wallet.getSigner().getPublicKeyFromDerivation({
    type: KeyDerivationType.LEAF,
    path: leafId,
  });
  const depositResp = await depositService.generateDepositAddress({
    signingPubkey: pubKey,
    leafId,
  });

  if (!depositResp.depositAddress) {
    throw new RPCError("Deposit address not found", {
      method: "generateDepositAddress",
      params: { signingPubkey: pubKey, leafId },
    });
  }

  const depositTx = new Transaction();
  depositTx.addInput(faucetCoin!.outpoint);

  // Add the main output
  const addr = Address(getNetwork(Network.LOCAL)).decode(
    depositResp.depositAddress.address,
  );
  const script = OutScript.encode(addr);
  depositTx.addOutput({ script, amount: amountSats });

  let treeResp: FinalizeNodeSignaturesResponse;
  if (includeDirectTx) {
    treeResp = await depositService.createTreeRoot({
      keyDerivation: {
        type: KeyDerivationType.LEAF,
        path: leafId,
      },
      verifyingKey: depositResp.depositAddress.verifyingKey,
      depositTx,
      vout: 0,
    });
  } else {
    treeResp = await depositService.createTreeWithoutDirectTx({
      keyDerivation: {
        type: KeyDerivationType.LEAF,
        path: leafId,
      },
      verifyingKey: depositResp.depositAddress.verifyingKey,
      depositTx,
      vout: 0,
    });
  }

  const signedDepositTx = await faucet.signFaucetCoin(
    depositTx,
    faucetCoin!.txout,
    faucetCoin!.key,
  );

  await faucet.broadcastTx(signedDepositTx.hex);

  // Mine just 1 block instead of waiting for many confirmations
  const randomKey = secp256k1.utils.randomPrivateKey();
  const randomPubKey = secp256k1.getPublicKey(randomKey);
  const randomAddress = getP2TRAddressFromPublicKey(
    randomPubKey,
    Network.LOCAL,
  );

  await faucet.generateToAddress(1, randomAddress);

  await new Promise((resolve) => setTimeout(resolve, 100));
  return treeResp.nodes[0]!;
}

export async function createNewTree(
  wallet: SparkWalletTesting,
  leafId: string,
  faucet: BitcoinFaucet,
  amountSats: bigint = 100_000n,
): Promise<TreeNode> {
  return await createDeposit(wallet, leafId, faucet, amountSats, true);
}

export async function createNewTreeWithoutDirectTx(
  wallet: SparkWalletTesting,
  leafId: string,
  faucet: BitcoinFaucet,
  amountSats: bigint = 100_000n,
): Promise<TreeNode> {
  return await createDeposit(wallet, leafId, faucet, amountSats, false);
}

export const signerTypes = [
  {
    name: "DefaultSparkSigner",
    Signer: DefaultSparkSigner,
    createTree: createNewTree,
  },
  {
    name: "UnsafeStatelessSparkSigner",
    Signer: UnsafeStatelessSparkSigner,
    createTree: createNewTree,
  },
];

export const walletTypes = [
  ...signerTypes,
  {
    name: "DefaultSparkSignerWithoutDirectTx",
    Signer: UnsafeStatelessSparkSigner,
    createTree: createNewTreeWithoutDirectTx,
  },
];

export function createDeterministicKeys(seed: string) {
  const privateKey = sha256(new TextEncoder().encode(seed));
  const publicKey = secp256k1.getPublicKey(privateKey, true);
  return { privateKey, publicKey };
}
