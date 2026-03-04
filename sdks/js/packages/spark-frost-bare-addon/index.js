const binding = require("./binding");

exports.hello = binding.hello;
exports.createDummyTx = binding.createDummyTx;
exports.encryptEcies = binding.encryptEcies;
exports.decryptEcies = binding.decryptEcies;
exports.signFrost = binding.signFrost;
exports.aggregateFrost = binding.aggregateFrost;
exports.splitSecretWithProofs = binding.splitSecretWithProofs;
exports.recoverSecret = binding.recoverSecret;
exports.validateShare = binding.validateShare;
exports.constructNodeTxPair = binding.constructNodeTxPair;
exports.constructRefundTxTrio = binding.constructRefundTxTrio;
exports.computeMultiInputSighash = binding.computeMultiInputSighash;
