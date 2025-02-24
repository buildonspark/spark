import * as btc from "@scure/btc-signer";
import { Network as NetworkProto } from "../proto/spark.js";
export var Network;
(function (Network) {
    Network[Network["MAINNET"] = 0] = "MAINNET";
    Network[Network["TESTNET"] = 1] = "TESTNET";
    Network[Network["SIGNET"] = 2] = "SIGNET";
    Network[Network["REGTEST"] = 3] = "REGTEST";
    Network[Network["LOCAL"] = 4] = "LOCAL";
})(Network || (Network = {}));
export const NetworkToProto = {
    [Network.MAINNET]: NetworkProto.MAINNET,
    [Network.TESTNET]: NetworkProto.TESTNET,
    [Network.SIGNET]: NetworkProto.SIGNET,
    [Network.REGTEST]: NetworkProto.REGTEST,
    [Network.LOCAL]: NetworkProto.REGTEST,
};
const NetworkConfig = {
    [Network.MAINNET]: btc.NETWORK,
    [Network.TESTNET]: btc.TEST_NETWORK,
    [Network.SIGNET]: btc.TEST_NETWORK,
    [Network.REGTEST]: { ...btc.TEST_NETWORK, bech32: "bcrt" },
    [Network.LOCAL]: { ...btc.TEST_NETWORK, bech32: "bcrt" },
};
export const getNetwork = (network) => NetworkConfig[network];
//# sourceMappingURL=network.js.map