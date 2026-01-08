import { SparkWallet } from "@buildonspark/spark-sdk";

export type InvoiceFromRootEsm = Awaited<
  ReturnType<InstanceType<typeof SparkWallet>["createLightningInvoice"]>
>;
