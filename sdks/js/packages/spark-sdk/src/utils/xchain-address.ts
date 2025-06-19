import * as bitcoin from 'bitcoinjs-lib';
import { encodeSparkAddress, SparkAddressFormat } from '../address/address.js';
import { ValidationError } from '../index.node.js';

const networkByType = {
  bitcoin: 'MAINNET',
  regtest: 'REGTEST',
  testnet: 'TESTNET',
} as const;

export function getSparkAddressFromTaproot(taprootAddress: string): SparkAddressFormat {
  for (const networkType of ['bitcoin', 'regtest', 'testnet'] as const) {
    const network = bitcoin.networks[networkType];
    try {
      bitcoin.payments.p2tr({address: taprootAddress, network});
    } catch (_) {
      continue;
    }
    const { data: outputPublicKey } = bitcoin.address.fromBech32(taprootAddress);
    return encodeSparkAddress({
      identityPublicKey: Buffer.concat([Buffer.from([0x02]), outputPublicKey]).toString('hex'),
      network: networkByType[networkType],
    })
  }

  throw new ValidationError('Invalid taproot address');
}
