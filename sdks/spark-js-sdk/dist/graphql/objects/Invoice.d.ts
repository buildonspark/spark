import BitcoinNetwork from './BitcoinNetwork.js';
import CurrencyAmount from './CurrencyAmount.js';
interface Invoice {
    encodedEnvoice: string;
    bitcoinNetwork: BitcoinNetwork;
    paymentHash: string;
    amount: CurrencyAmount;
    createdAt: string;
    expiresAt: string;
    memo?: string | undefined;
}
export declare const InvoiceFromJson: (obj: any) => Invoice;
export declare const InvoiceToJson: (obj: Invoice) => any;
export declare const FRAGMENT = "\nfragment InvoiceFragment on Invoice {\n    __typename\n    invoice_encoded_envoice: encoded_envoice\n    invoice_bitcoin_network: bitcoin_network\n    invoice_payment_hash: payment_hash\n    invoice_amount: amount {\n        __typename\n        currency_amount_original_value: original_value\n        currency_amount_original_unit: original_unit\n        currency_amount_preferred_currency_unit: preferred_currency_unit\n        currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded\n        currency_amount_preferred_currency_value_approx: preferred_currency_value_approx\n    }\n    invoice_created_at: created_at\n    invoice_expires_at: expires_at\n    invoice_memo: memo\n}";
export default Invoice;
