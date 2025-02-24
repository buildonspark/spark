/** This is an enum identifying a particular Bitcoin Network. **/
export declare enum BitcoinNetwork {
    /**
     * This is an enum value that represents values that could be added in the future.
     * Clients should support unknown values as more of them could be added without notice.
     */
    FUTURE_VALUE = "FUTURE_VALUE",
    /** The production version of the Bitcoin Blockchain. **/
    MAINNET = "MAINNET",
    /** A test version of the Bitcoin Blockchain, maintained by Lightspark. **/
    REGTEST = "REGTEST",
    /** A test version of the Bitcoin Blockchain, maintained by a centralized organization. Not in use at Lightspark. **/
    SIGNET = "SIGNET",
    /** A test version of the Bitcoin Blockchain, publicly available. **/
    TESTNET = "TESTNET"
}
export default BitcoinNetwork;
