import { TokenLeafCreationData } from "spark-js-sdk/src/services/tokens";
import { bytesToNumberBE, numberToBytesBE } from "@noble/curves/abstract/utils";
import { constructMintTransaction } from "../utils/transaction";
import { secp256k1 } from "@noble/curves/secp256k1";

describe("construct issuence transaction", () => {
    it("should construct a valid token transaction", () => {
      const tokenAmount: bigint = 1000n;
      const tokenPrivateKey = secp256k1.utils.randomPrivateKey();
      const tokenPublicKey = secp256k1.getPublicKey(tokenPrivateKey, false);
  
      const tokenLeafData: TokenLeafCreationData[] = [
        {
          tokenPublicKey: tokenPublicKey,
          tokenAmount: numberToBytesBE(tokenAmount, 16),
          withdrawalBondSats: 10000,
          withdrawalLocktime: 100,
        },
      ];
  
      const transaction = constructMintTransaction(
        tokenLeafData
      );
  
      expect(transaction).toBeDefined();
      expect(transaction.tokenInput!.$case).toEqual("mintInput");
      expect(transaction.outputLeaves).toHaveLength(1);
      expect(transaction.outputLeaves[0].ownerPublicKey).toEqual(tokenPublicKey);
      expect(bytesToNumberBE(transaction.outputLeaves[0].tokenAmount)).toEqual(
        tokenAmount
      );
      expect(transaction.outputLeaves[0].withdrawalBondSats).toEqual(
        tokenLeafData[0].withdrawalBondSats
      );
      expect(transaction.outputLeaves[0].withdrawalLocktime).toEqual(
        tokenLeafData[0].withdrawalLocktime
      );
    });
  });