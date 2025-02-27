import { SparkWallet } from "@buildonspark/spark-sdk";
import { Network } from "@buildonspark/spark-sdk/utils";

console.log("Spark SDK Example");

const network = Network.REGTEST;
const wallet = new SparkWallet(network);

console.log("Network:", network);
console.log("Wallet:", wallet);
