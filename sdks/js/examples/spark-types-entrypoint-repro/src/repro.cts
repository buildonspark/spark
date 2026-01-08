import type { LightningReceiveRequest } from "@buildonspark/spark-sdk/types";
import { SparkWallet } from "@buildonspark/spark-sdk";
import type { InvoiceFromRootEsm } from "./root-esm.mjs";

export type InvoiceFromRoot = Awaited<
  ReturnType<InstanceType<typeof SparkWallet>["createLightningInvoice"]>
>;

// This will pass:
// declare const invoiceFromEsmGraph: InvoiceFromRoot;
// This will fail:
declare const invoiceFromEsmGraph: InvoiceFromRootEsm;

// This assignment is expected to fail in affected versions because:
// - `InvoiceFromRootEsm` is resolved through the ESM declaration graph, while
// - `LightningReceiveRequest` (in this `.cts` file) is resolved through the CJS graph.
const _typed: LightningReceiveRequest = invoiceFromEsmGraph;

void _typed;
