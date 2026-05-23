import { isError } from "@lightsparkdev/core";
import { bytesToHex } from "@noble/curves/utils";
import express from "express";
import issuerRoutes from "./routes/issuerRoutes.js";
import sparkRoutes from "./routes/sparkRoutes.js";

const app = express();

enum BitcoinNetwork {
  MAINNET = "MAINNET",
  REGTEST = "REGTEST",
}
export const BITCOIN_NETWORK = BitcoinNetwork.REGTEST;

app.use(express.json());
// parse bigint and Uint8Array to string
app.use((req, res, next) => {
  res.json = function (data: unknown) {
    return res.send(
      JSON.stringify(data, (_key: string, value: unknown): unknown => {
        if (typeof value === "bigint") {
          return value.toString();
        } else if (value instanceof Uint8Array) {
          return bytesToHex(value);
        }
        return value;
      }),
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

function isAddressInUseError(err: unknown): err is NodeJS.ErrnoException {
  return isError(err) && "code" in err && err.code === "EADDRINUSE";
}

function startServer(port: number) {
  if (port > maxPort) {
    console.error("No available ports found in range");
    process.exit(1);
    return;
  }
  app
    .listen(port)
    .on("listening", () => {
      console.log(`Spark API running on port ${port}`);
    })
    .on("error", (err) => {
      const errorMsg = isError(err) ? err.message : "Unknown error";
      if (isAddressInUseError(err)) {
        console.log(`Port ${port} is busy, trying ${port + 1}...`);
        startServer(port + 1);
      } else {
        console.error("Server error:", errorMsg);
      }
    });
}

startServer(startPort);
