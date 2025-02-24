import { DefaultSparkSigner } from "../signer/signer.js";
import { LOCAL_WALLET_CONFIG, REGTEST_WALLET_CONFIG, } from "../tests/test-util.js";
import { Network, NetworkToProto } from "../utils/network.js";
export class WalletConfigService {
    config;
    signer;
    constructor(network, signer) {
        // TODO: differentiate between mainnet, regtest, and local
        // local config is LOCAL_WALLET_CONFIG - uses local signing operators
        console.log("network", network === Network.LOCAL);
        this.config =
            network === Network.LOCAL ? LOCAL_WALLET_CONFIG : REGTEST_WALLET_CONFIG;
        this.signer = signer || new DefaultSparkSigner();
    }
    getCoordinatorAddress() {
        return this.config.signingOperators[this.config.coodinatorIdentifier]
            .address;
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