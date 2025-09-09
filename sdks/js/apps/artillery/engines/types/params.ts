type NetworkType = "MAINNET" | "REGTEST" | "TESTNET" | "SIGNET" | "LOCAL";

export interface DepositParams {
  amount?: number;
  expectBalance?: number;
}

export interface TransferParams {
  amount: number;
  receiverAddress?: string;
}

export interface WalletParams {
  network?: NetworkType;
  mnemonic?: string;
  walletName?: string;
}

export interface LightningInvoiceParams {
  amountSats: number;
  memo?: string;
}

export interface AddressParams {
  network: NetworkType;
  seedOrMnemonicList: string[];
  sparkAddressPrefix: string;
  blockchainAddressPrefix: string;
}

export interface MultipleDepositParams {
  amounts: number[];
}

export interface AdvancedDepositParams {
  amount: number;
}
