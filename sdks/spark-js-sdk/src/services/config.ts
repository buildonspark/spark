import { secp256k1 } from "@noble/curves/secp256k1";
import { Network } from "../utils/network";

export type SigningOperator = {
  id: number;
  identifier: string;
  address: string;
  identityPublicKey: Uint8Array;
};

export type WalletConfig = {
  network: Network;
  signingOperators: Record<string, SigningOperator>;
  coodinatorIdentifier: string;
  frostSignerAddress: string;
  identityPrivateKey: Uint8Array;
  threshold: number;
};

export class WalletConfigService {
  private config: WalletConfig;

  constructor(config: WalletConfig) {
    this.config = config;
  }

  getCoordinatorAddress() {
    return this.config.signingOperators[this.config.coodinatorIdentifier]
      .address;
  }

  getIdentityPublicKey() {
    return secp256k1.getPublicKey(this.config.identityPrivateKey);
  }

  getConfig() {
    return this.config;
  }
}
