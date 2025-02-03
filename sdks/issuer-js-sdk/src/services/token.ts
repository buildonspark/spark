import type { LRCWallet } from '@wcbd/yuv-js-sdk';
import { SparkSDK } from '../../../spark-js-sdk/src/spark-sdk';

export async function issue(
  tokenPublicKey: string,
  amountToMint: number,
  mintDestinationAddress: string,
  wallet: LRCWallet,
): Promise<void> {
  const tokenTransaction = {
    leavesToSpend: [], // Empty for initial issuance
    leavesToCreate: [{
      id: 
      verifyingPublicKey: 
      ownerIdentityPublicKey: Buffer.from(mintDestinationAddress, 'hex'), // Usually same as verifying key
      revocationPublicKey: undefined, // what is this about
      withdrawalFeeRateVb: BigInt(0), // No fee for initial issuance
      withdrawalBondSats: BigInt(1000),
      withdrawalLocktime: BigInt(Math.floor(Date.now() / 1000) + 86400),
      tokenId: tokenPublicKey,
      tokenAmount: BigInt(amountToMint)
    }]
  };

  let sparkSdk = new SparkSDK();

  let tokenTransactionHash = await sparkSdk.signFrost(
    {
      msg: tokenTransaction,
      keyPackage: 
      nonce: 
      selfCommitment: 
      statechainCommitments: 
    }
  )

  const signingJob = {
    signingPublicKey: Buffer.from(, 'hex'),
    tokenTransactionHash: 
    signingNonceCommitment: {
      commitment: 
      proof: 
    }
  };

  await wallet.startTokenTransaction(tokenTransaction, signingJob);
}

export async function transfer(
  tokenPublicKey: string,
  amountToTransfer: number,
  transferDestinationAddress: string,
  wallet: LRCWallet
): Promise<void> {
  let revocationKey: Buffer = await wallet.generateRevocationKey(Buffer.from(tokenPublicKey, 'hex'));


}

export async function deposit(
  tokenPublicKey: string,
  amountToTransfer: number,
  transferDestinationAddress: string,
  wallet: LRCWallet
): Promise<void> {
  let depositAddress = wallet.getDepositAddress(Buffer.from(tokenPublicKey, 'hex'));

}
