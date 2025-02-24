// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved
/** This is an enum identifying a particular Bitcoin Network. **/
export var BitcoinNetwork;
(function (BitcoinNetwork) {
    /**
     * This is an enum value that represents values that could be added in the future.
     * Clients should support unknown values as more of them could be added without notice.
     */
    BitcoinNetwork["FUTURE_VALUE"] = "FUTURE_VALUE";
    /** The production version of the Bitcoin Blockchain. **/
    BitcoinNetwork["MAINNET"] = "MAINNET";
    /** A test version of the Bitcoin Blockchain, maintained by Lightspark. **/
    BitcoinNetwork["REGTEST"] = "REGTEST";
    /** A test version of the Bitcoin Blockchain, maintained by a centralized organization. Not in use at Lightspark. **/
    BitcoinNetwork["SIGNET"] = "SIGNET";
    /** A test version of the Bitcoin Blockchain, publicly available. **/
    BitcoinNetwork["TESTNET"] = "TESTNET";
})(BitcoinNetwork || (BitcoinNetwork = {}));
export default BitcoinNetwork;
//# sourceMappingURL=BitcoinNetwork.js.map