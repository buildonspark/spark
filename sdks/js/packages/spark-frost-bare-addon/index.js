const binding = require("./binding");

exports.hello = binding.hello;
exports.createDummyTx = binding.createDummyTx;
exports.encryptEcies = binding.encryptEcies;
exports.decryptEcies = binding.decryptEcies;
exports.signFrost = binding.signFrost;
exports.aggregateFrost = binding.aggregateFrost;
