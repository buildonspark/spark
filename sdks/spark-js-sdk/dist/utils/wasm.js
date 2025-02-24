import { construct_node_tx, construct_refund_tx, construct_split_tx, create_dummy_tx, decrypt_ecies, encrypt_ecies, wasm_aggregate_frost, wasm_sign_frost, } from "../wasm/spark_bindings.js";
export function signFrost({ msg, keyPackage, nonce, selfCommitment, statechainCommitments, adaptorPubKey, }) {
    return wasm_sign_frost(msg, keyPackage, nonce, selfCommitment, statechainCommitments, adaptorPubKey);
}
export function aggregateFrost({ msg, statechainCommitments, selfCommitment, statechainSignatures, selfSignature, statechainPublicKeys, selfPublicKey, verifyingKey, adaptorPubKey, }) {
    return wasm_aggregate_frost(msg, statechainCommitments, selfCommitment, statechainSignatures, selfSignature, statechainPublicKeys, selfPublicKey, verifyingKey, adaptorPubKey);
}
export function constructNodeTx({ tx, vout, address, locktime, }) {
    return construct_node_tx(tx, vout, address, locktime);
}
export function constructRefundTx({ tx, vout, pubkey, network, locktime, }) {
    return construct_refund_tx(tx, vout, pubkey, network, locktime);
}
export function constructSplitTx({ tx, vout, addresses, locktime, }) {
    return construct_split_tx(tx, vout, addresses, locktime);
}
export function createDummyTx({ address, amountSats, }) {
    return create_dummy_tx(address, amountSats);
}
export function encryptEcies({ msg, publicKey, }) {
    return encrypt_ecies(msg, publicKey);
}
export function decryptEcies({ encryptedMsg, privateKey, }) {
    return decrypt_ecies(encryptedMsg, privateKey);
}
//# sourceMappingURL=wasm.js.map