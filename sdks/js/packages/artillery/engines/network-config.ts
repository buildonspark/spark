import { ConfigOptions } from "@buildonspark/spark-sdk";
import fs from "fs";

export function getLoadtestNetworkConfig(): ConfigOptions {
  if (process.env.CONFIG_FILE) {
    try {
      const data = fs.readFileSync(process.env.CONFIG_FILE, "utf8");
      let config: ConfigOptions = JSON.parse(data);

      if (config.network !== "REGTEST") {
        throw "Only REGTEST network is supported for loadtest.";
      }

      return config;
    } catch (err) {
      throw new Error(`Error reading or parsing config file: ${err}`);
    }
  }

  throw new Error("Missing CONFIG_FILE environment variable");
}
