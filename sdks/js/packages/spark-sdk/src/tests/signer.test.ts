import { HDKey } from "@scure/bip32";
import { TaprootOutputKeysGenerator } from "../signer/signer.js";
import * as btc from '@scure/btc-signer';

describe("signer", () => {
  let randomSeed = new Uint8Array(32);

  beforeAll(() => {
    crypto.getRandomValues(randomSeed);
  });

  it.each([0, 1, 2, 3, 4, 5, 6, 7, 8, 9])("TaprootOutputKeysGenerator account %d", async (accountNumber) => {
    const masterSeed = HDKey.fromMasterSeed(randomSeed);
    const taprootOutputKeys = await new TaprootOutputKeysGenerator()
      .deriveKeysFromSeed(randomSeed, accountNumber);

    const taprootInternalKey = masterSeed.derive(`m/86'/0'/${accountNumber}'/0/0`)!;
    const xOnlyPublicKey = taprootInternalKey.publicKey!.slice(-32);
    expect(Buffer.from(taprootOutputKeys.identityKey.publicKey).toString('hex'))
      .toBe('02' + Buffer.from(btc.p2tr(xOnlyPublicKey).tweakedPubkey).toString('hex'));
  });
});
