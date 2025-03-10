import { SparkWallet } from "@buildonspark/spark-sdk";

console.log("Spark SDK Example");

const network = "REGTEST";
const wallet = new SparkWallet(network);

console.log("Network:", network);
