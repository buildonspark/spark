import { isError } from "@lightsparkdev/core";
import { bytesToHex } from "@noble/curves/utils";
import express from "express";
import issuerRoutes from "../routes/issuerRoutes.js";
import sparkRoutes from "../routes/sparkRoutes.js";

const app = express();

enum BitcoinNetwork {
  MAINNET = "MAINNET",
  REGTEST = "REGTEST",
}
export const BITCOIN_NETWORK = BitcoinNetwork.REGTEST;

app.use(express.json());
// parse bigint and Uint8Array to string
app.use((req, res, next) => {
  res.json = function (data) {
    return res.send(
      JSON.stringify(data, (key, value) => {
        if (typeof value === "bigint") {
          return value.toString();
        } else if (value instanceof Uint8Array) {
          return bytesToHex(value);
        }
        return value;
      })
    );
  };
  next();
});

app.use("/spark-wallet", sparkRoutes);
app.use("/issuer-wallet", issuerRoutes);

app.get("/", (req, res) => {
  res.send("Hello World");
});

const startPort = 4000;
const maxPort = 4010;

function startServer(port: number) {
  if (port > maxPort) {
    console.error("No available ports found in range");
    process.exit(1);
    return;
  }
  const server = app
    .listen(port)
    .on("listening", () => {
      console.log(`Spark API running on port ${port}`);
    })
    .on("error", (err) => {
      const errorMsg = isError(err) ? err.message : "Unknown error";
      if (isError(err) && (err as any).code === "EADDRINUSE") {
        console.log(`Port ${port} is busy, trying ${port + 1}...`);
        startServer(port + 1);
      } else {
        console.error("Server error:", errorMsg);
      }
    });
}

startServer(startPort);
