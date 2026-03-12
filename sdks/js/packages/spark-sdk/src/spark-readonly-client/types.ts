import { TransferType } from "../proto/spark.js";

export type QueryTransfersParams = {
  sparkAddress: string;
  limit?: number;
  offset?: number;
  types?: TransferType[];
  createdAfter?: Date;
  createdBefore?: Date;
};

export type QueryDepositAddressesParams = {
  sparkAddress: string;
  limit?: number;
  offset?: number;
};

export type GetUtxosParams = {
  depositAddress: string;
  limit?: number;
  offset?: number;
  excludeClaimed?: boolean;
};

export type GetUtxosForIdentityParams = {
  identityPublicKey: string;
  pageSize?: number;
  cursor?: string;
  direction?: "NEXT" | "PREVIOUS";
  excludeClaimed?: boolean;
  includePending?: boolean;
};

export type WalletGetUtxosForIdentityParams = Omit<
  GetUtxosForIdentityParams,
  "identityPublicKey"
> & {
  identityPublicKey?: string;
};

export type QuerySparkInvoicesParams = {
  invoices: string[];
  limit?: number;
  offset?: number;
};

export type QueryTokenTransactionsParams = {
  sparkAddresses?: string[];
  issuerPublicKeys?: string[];
  tokenIdentifiers?: string[];
  outputIds?: string[];
  pageSize?: number;
  cursor?: string;
  direction?: "NEXT" | "PREVIOUS";
};
