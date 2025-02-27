export enum CurrencyType {
  FIAT = "FIAT",
  BLOCKCHAIN = "BLOCKCHAIN",
  TOKEN = "TOKEN",
}

export interface Currency {
  type: CurrencyType;
  name: string;
  code?: string;
  decimals?: number;
  symbol?: string;
  balance?: number;
  logo?: React.ReactNode;
  pubkey?: string;
  usdPrice?: number;
}
