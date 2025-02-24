import * as btc from "@scure/btc-signer";
import { Network as NetworkProto } from "../proto/spark.js";
export declare enum Network {
    MAINNET = 0,
    TESTNET = 1,
    SIGNET = 2,
    REGTEST = 3,
    LOCAL = 4
}
export declare const NetworkToProto: Record<Network, NetworkProto>;
export declare const getNetwork: (network: Network) => typeof btc.NETWORK;
