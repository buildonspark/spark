import { DefaultSparkSigner } from "../signer/signer.js";
import { LOCAL_WALLET_CONFIG, REGTEST_WALLET_CONFIG, } from "../tests/test-util.js";
import { Network, NetworkToProto } from "../utils/network.js";
export class WalletConfigService {
    config;
    signer;
    constructor(network, signer) {
        this.config =
            network === Network.LOCAL ? LOCAL_WALLET_CONFIG : REGTEST_WALLET_CONFIG;
        this.signer = signer || new DefaultSparkSigner();
    }
    getCoordinatorAddress() {
        const coordinator = this.config.signingOperators[this.config.coodinatorIdentifier];
        if (!coordinator) {
            throw new Error(`Coordinator ${this.config.coodinatorIdentifier} not found`);
        }
        return coordinator.address;
    }
    getConfig() {
        return this.config;
    }
    getNetwork() {
        return this.config.network;
    }
    getNetworkProto() {
        return NetworkToProto[this.config.network];
    }
}
//# sourceMappingURL=config.js.map