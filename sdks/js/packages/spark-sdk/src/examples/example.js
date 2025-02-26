"use strict";
var __awaiter = (this && this.__awaiter) || function (thisArg, _arguments, P, generator) {
    function adopt(value) { return value instanceof P ? value : new P(function (resolve) { resolve(value); }); }
    return new (P || (P = Promise))(function (resolve, reject) {
        function fulfilled(value) { try { step(generator.next(value)); } catch (e) { reject(e); } }
        function rejected(value) { try { step(generator["throw"](value)); } catch (e) { reject(e); } }
        function step(result) { result.done ? resolve(result.value) : adopt(result.value).then(fulfilled, rejected); }
        step((generator = generator.apply(thisArg, _arguments || [])).next());
    });
};
var __generator = (this && this.__generator) || function (thisArg, body) {
    var _ = { label: 0, sent: function() { if (t[0] & 1) throw t[1]; return t[1]; }, trys: [], ops: [] }, f, y, t, g = Object.create((typeof Iterator === "function" ? Iterator : Object).prototype);
    return g.next = verb(0), g["throw"] = verb(1), g["return"] = verb(2), typeof Symbol === "function" && (g[Symbol.iterator] = function() { return this; }), g;
    function verb(n) { return function (v) { return step([n, v]); }; }
    function step(op) {
        if (f) throw new TypeError("Generator is already executing.");
        while (g && (g = 0, op[0] && (_ = 0)), _) try {
            if (f = 1, y && (t = op[0] & 2 ? y["return"] : op[0] ? y["throw"] || ((t = y["return"]) && t.call(y), 0) : y.next) && !(t = t.call(y, op[1])).done) return t;
            if (y = 0, t) op = [op[0] & 2, t.value];
            switch (op[0]) {
                case 0: case 1: t = op; break;
                case 4: _.label++; return { value: op[1], done: false };
                case 5: _.label++; y = op[1]; op = [0]; continue;
                case 7: op = _.ops.pop(); _.trys.pop(); continue;
                default:
                    if (!(t = _.trys, t = t.length > 0 && t[t.length - 1]) && (op[0] === 6 || op[0] === 2)) { _ = 0; continue; }
                    if (op[0] === 3 && (!t || (op[1] > t[0] && op[1] < t[3]))) { _.label = op[1]; break; }
                    if (op[0] === 6 && _.label < t[1]) { _.label = t[1]; t = op; break; }
                    if (t && _.label < t[2]) { _.label = t[2]; _.ops.push(op); break; }
                    if (t[2]) _.ops.pop();
                    _.trys.pop(); continue;
            }
            op = body.call(thisArg, _);
        } catch (e) { op = [6, e]; y = 0; } finally { f = t = 0; }
        if (op[0] & 5) throw op[1]; return { value: op[0] ? op[1] : void 0, done: true };
    }
};
Object.defineProperty(exports, "__esModule", { value: true });
var utils_1 = require("@noble/curves/abstract/utils");
var readline_1 = require("readline");
var objects_1 = require("../../dist/graphql/objects");
var spark_sdk_1 = require("../../dist/spark-sdk");
var bitcoin_1 = require("../../dist/utils/bitcoin");
var network_1 = require("../../dist/utils/network");
// Initialize Spark Wallet
var walletMnemonic = "typical stereo dose party penalty decline neglect feel harvest abstract stage winter";
function runCLI() {
    return __awaiter(this, void 0, void 0, function () {
        var wallet, rl, helpMessage, _loop_1, state_1;
        var _a, _b;
        return __generator(this, function (_c) {
            switch (_c.label) {
                case 0:
                    wallet = new spark_sdk_1.SparkWallet(network_1.Network.REGTEST);
                    rl = readline_1.default.createInterface({
                        input: process.stdin,
                        output: process.stdout,
                    });
                    helpMessage = "\n  Available commands:\n  genmnemonic                                     - Generate a new mnemonic\n  initwallet <mnemonic>                           - Create a new wallet from a mnemonic\n  gendepositaddr                                  - Generate a new deposit address\n  completedeposit <pubkey> <verifyingKey> <rawtx> - Complete a deposit\n  createinvoice <amount> <memo>                   - Create a new lightning invoice\n  payinvoice <invoice> <amount>                   - Pay a lightning invoice\n  balance                                         - Show current wallet balance\n  getleaves                                       - Show current leaves\n  sendtransfer <amount> <receiverPubKey>          - Send a transfer\n  pendingtransfers                                - Show pending transfers\n  claimtransfer <transferId>                      - Claim a pending transfer\n  help                                            - Show this help message\n  exit/quit                                       - Exit the program\n";
                    console.log(helpMessage);
                    _loop_1 = function () {
                        var command, _d, firstWord, rest, args, lowerCommand, _e, mnemonic, pubKey, leafPubKey, depositAddress, depositTx, treeResp, invoice, fee, receiverPubKey, amount, pending_1, pendingTransfers, transfer, result, payResult, balance, leaves;
                        return __generator(this, function (_f) {
                            switch (_f.label) {
                                case 0: return [4 /*yield*/, new Promise(function (resolve) {
                                        rl.question("> ", resolve);
                                    })];
                                case 1:
                                    command = _f.sent();
                                    _d = command.split(" "), firstWord = _d[0], rest = _d.slice(1);
                                    args = rest.join(" ");
                                    lowerCommand = firstWord.toLowerCase();
                                    if (lowerCommand === "exit" || lowerCommand === "quit") {
                                        rl.close();
                                        return [2 /*return*/, "break"];
                                    }
                                    _e = lowerCommand;
                                    switch (_e) {
                                        case "help": return [3 /*break*/, 2];
                                        case "genmnemonic": return [3 /*break*/, 3];
                                        case "initwallet": return [3 /*break*/, 4];
                                        case "gendepositaddr": return [3 /*break*/, 6];
                                        case "completedeposit": return [3 /*break*/, 8];
                                        case "createinvoice": return [3 /*break*/, 10];
                                        case "sendtransfer": return [3 /*break*/, 13];
                                        case "pendingtransfers": return [3 /*break*/, 15];
                                        case "claimtransfer": return [3 /*break*/, 17];
                                        case "payinvoice": return [3 /*break*/, 20];
                                        case "balance": return [3 /*break*/, 22];
                                        case "getleaves": return [3 /*break*/, 24];
                                    }
                                    return [3 /*break*/, 26];
                                case 2:
                                    console.log(helpMessage);
                                    return [3 /*break*/, 26];
                                case 3:
                                    mnemonic = wallet.generateMnemonic();
                                    console.log(mnemonic);
                                    return [3 /*break*/, 26];
                                case 4:
                                    console.log(":".concat(args, ":"));
                                    return [4 /*yield*/, wallet.createSparkWallet(args || walletMnemonic)];
                                case 5:
                                    pubKey = _f.sent();
                                    console.log("pubkey", pubKey);
                                    return [3 /*break*/, 26];
                                case 6:
                                    if (!wallet.isInitialized()) {
                                        console.log("No wallet initialized");
                                        return [3 /*break*/, 26];
                                    }
                                    leafPubKey = wallet.getSigner().generatePublicKey();
                                    return [4 /*yield*/, wallet.generateDepositAddress(leafPubKey)];
                                case 7:
                                    depositAddress = _f.sent();
                                    console.log("Deposit address:", (_a = depositAddress.depositAddress) === null || _a === void 0 ? void 0 : _a.address);
                                    console.log("Verifying key:", (0, utils_1.bytesToHex)(((_b = depositAddress.depositAddress) === null || _b === void 0 ? void 0 : _b.verifyingKey) || new Uint8Array()));
                                    console.log("Pubkey:", (0, utils_1.bytesToHex)(leafPubKey));
                                    return [3 /*break*/, 26];
                                case 8:
                                    if (!wallet.isInitialized()) {
                                        console.log("No wallet initialized");
                                        return [3 /*break*/, 26];
                                    }
                                    depositTx = (0, bitcoin_1.getTxFromRawTxHex)(args[2]);
                                    return [4 /*yield*/, wallet.createTreeRoot((0, utils_1.hexToBytes)(args[0]), (0, utils_1.hexToBytes)(args[1]), depositTx, 0)];
                                case 9:
                                    treeResp = _f.sent();
                                    console.log("Tree root:", treeResp.nodes);
                                    return [3 /*break*/, 26];
                                case 10:
                                    if (!wallet.isInitialized()) {
                                        console.log("No wallet initialized");
                                        return [3 /*break*/, 26];
                                    }
                                    return [4 /*yield*/, wallet.createLightningInvoice({
                                            amountSats: parseInt(args),
                                            memo: args[1],
                                            expirySeconds: 60 * 60 * 24,
                                        })];
                                case 11:
                                    invoice = _f.sent();
                                    return [4 /*yield*/, wallet.getLightningReceiveFeeEstimate({
                                            amountSats: parseInt(args),
                                            network: objects_1.BitcoinNetwork.REGTEST,
                                        })];
                                case 12:
                                    fee = _f.sent();
                                    console.log("Invoice created:", invoice);
                                    console.log("Fee: ".concat(fee === null || fee === void 0 ? void 0 : fee.feeEstimate.originalValue, " ").concat(fee === null || fee === void 0 ? void 0 : fee.feeEstimate.originalUnit));
                                    return [3 /*break*/, 26];
                                case 13:
                                    if (!wallet.isInitialized()) {
                                        console.log("No wallet initialized");
                                        return [3 /*break*/, 26];
                                    }
                                    receiverPubKey = (0, utils_1.hexToBytes)(args[1]);
                                    amount = parseInt(args[0]);
                                    return [4 /*yield*/, wallet.sendTransfer(amount, receiverPubKey)];
                                case 14:
                                    _f.sent();
                                    return [3 /*break*/, 26];
                                case 15:
                                    if (!wallet.isInitialized()) {
                                        console.log("No wallet initialized");
                                        return [3 /*break*/, 26];
                                    }
                                    return [4 /*yield*/, wallet.queryPendingTransfers()];
                                case 16:
                                    pending_1 = _f.sent();
                                    console.log(pending_1);
                                    return [3 /*break*/, 26];
                                case 17:
                                    if (!wallet.isInitialized()) {
                                        console.log("No wallet initialized");
                                        return [3 /*break*/, 26];
                                    }
                                    if (!args) {
                                        console.log("Please provide a transfer id");
                                        return [3 /*break*/, 26];
                                    }
                                    return [4 /*yield*/, wallet.queryPendingTransfers()];
                                case 18:
                                    pendingTransfers = _f.sent();
                                    transfer = pendingTransfers.transfers.find(function (t) { return t.id === args; });
                                    if (!transfer) {
                                        console.log("Transfer not found");
                                        return [3 /*break*/, 26];
                                    }
                                    return [4 /*yield*/, wallet.claimTransfer(transfer)];
                                case 19:
                                    result = _f.sent();
                                    console.log(result.nodes);
                                    return [3 /*break*/, 26];
                                case 20:
                                    if (!wallet.isInitialized()) {
                                        console.log("No wallet initialized");
                                        return [3 /*break*/, 26];
                                    }
                                    return [4 /*yield*/, wallet.payLightningInvoice({
                                            invoice: args[0],
                                            idempotencyKey: args[0],
                                            amountSats: parseInt(args[1]),
                                        })];
                                case 21:
                                    payResult = _f.sent();
                                    console.log(payResult);
                                    return [3 /*break*/, 26];
                                case 22:
                                    if (!wallet.isInitialized()) {
                                        console.log("No wallet initialized");
                                        return [3 /*break*/, 26];
                                    }
                                    return [4 /*yield*/, wallet.getBalance()];
                                case 23:
                                    balance = _f.sent();
                                    console.log(balance);
                                    return [3 /*break*/, 26];
                                case 24:
                                    if (!wallet.isInitialized()) {
                                        console.log("No wallet initialized");
                                        return [3 /*break*/, 26];
                                    }
                                    return [4 /*yield*/, wallet.getLeaves()];
                                case 25:
                                    leaves = _f.sent();
                                    console.log(leaves);
                                    return [3 /*break*/, 26];
                                case 26: return [2 /*return*/];
                            }
                        });
                    };
                    _c.label = 1;
                case 1:
                    if (!true) return [3 /*break*/, 3];
                    return [5 /*yield**/, _loop_1()];
                case 2:
                    state_1 = _c.sent();
                    if (state_1 === "break")
                        return [3 /*break*/, 3];
                    return [3 /*break*/, 1];
                case 3: return [2 /*return*/];
            }
        });
    });
}
runCLI();
