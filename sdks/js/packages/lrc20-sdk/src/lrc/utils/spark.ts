import * as bitcoin from "bitcoinjs-lib";

export class TokenLeaf {
  id: string;
  verifyingPublicKey: Buffer;
  ownerIdentityPublicKey: Buffer;
  revocationPublicKey: Buffer;
  withdrawalFeeRateVb: number;
  withdrawalBondSats: number;
  withdrawalLocktime: number;
  tokenId: Buffer;
  tokenAmount: number;
}

export class TokenTransaction {
  leavesToSpend: TokenLeaf[];
  leavesToCreate: TokenLeaf[];
}

export function tokenLeafHash(tokenLeaf: TokenLeaf): Buffer {
  const withdrawalFeeRateVbBuffer = Buffer.alloc(8);
  const withdrawalBondSatsBuffer = Buffer.alloc(8);
  const withdrawalLocktimeBuffer = Buffer.alloc(8);
  const tokenAmountBuffer = Buffer.alloc(8);
  withdrawalFeeRateVbBuffer.writeBigInt64BE(BigInt(tokenLeaf.withdrawalFeeRateVb));
  withdrawalBondSatsBuffer.writeBigInt64BE(BigInt(tokenLeaf.withdrawalBondSats));
  withdrawalLocktimeBuffer.writeBigInt64BE(BigInt(tokenLeaf.withdrawalLocktime));
  tokenAmountBuffer.writeBigInt64BE(BigInt(tokenLeaf.tokenAmount));

  const hashData = Buffer.concat([
    bitcoin.crypto.sha256(tokenLeaf.verifyingPublicKey),
    bitcoin.crypto.sha256(tokenLeaf.ownerIdentityPublicKey),
    bitcoin.crypto.sha256(tokenLeaf.revocationPublicKey),
    bitcoin.crypto.sha256(tokenLeaf.verifyingPublicKey),
    bitcoin.crypto.sha256(withdrawalFeeRateVbBuffer),
    bitcoin.crypto.sha256(withdrawalBondSatsBuffer),
    bitcoin.crypto.sha256(withdrawalLocktimeBuffer),
    bitcoin.crypto.sha256(tokenLeaf.tokenId),
    bitcoin.crypto.sha256(tokenAmountBuffer),
  ]);

  return bitcoin.crypto.sha256(hashData);
}

export function tokenTransactionHash(tokenTransaction: TokenTransaction): Buffer {
  let hashes = [];
  const leavesToSpend = tokenTransaction.leavesToSpend;
  const leavesToCreate = tokenTransaction.leavesToCreate;

  for (let i = 0; i < leavesToSpend.length; i++) {
    hashes.push(tokenLeafHash(leavesToSpend[i]));
  }
  for (let i = 0; i < leavesToCreate.length; i++) {
    hashes.push(tokenLeafHash(leavesToCreate[i]));
  }

  let hashData = Buffer.concat(hashes);
  return bitcoin.crypto.sha256(hashData);
}
