
// Copyright ©, 2023-present, Lightspark Group, Inc. - All Rights Reserved


import CurrencyAmount from './CurrencyAmount.js';
import BitcoinNetwork from './BitcoinNetwork.js';
import {CurrencyAmountFromJson} from './CurrencyAmount.js';
import {CurrencyAmountToJson} from './CurrencyAmount.js';


interface Invoice {


    encodedInvoice: string;

    bitcoinNetwork: BitcoinNetwork;

    paymentHash: string;

    amount: CurrencyAmount;

    createdAt: string;

    expiresAt: string;

    memo?: string | undefined;




}

export const InvoiceFromJson = (obj: any): Invoice => {
    return {
        encodedInvoice: obj["invoice_encoded_invoice"],
        bitcoinNetwork: BitcoinNetwork[obj["invoice_bitcoin_network"]] ?? BitcoinNetwork.FUTURE_VALUE,
        paymentHash: obj["invoice_payment_hash"],
        amount: CurrencyAmountFromJson(obj["invoice_amount"]),
        createdAt: obj["invoice_created_at"],
        expiresAt: obj["invoice_expires_at"],
        memo: obj["invoice_memo"],

        } as Invoice;

}
export const InvoiceToJson = (obj: Invoice): any => {
return {
invoice_encoded_invoice: obj.encodedInvoice,
invoice_bitcoin_network: obj.bitcoinNetwork,
invoice_payment_hash: obj.paymentHash,
invoice_amount: CurrencyAmountToJson(obj.amount),
invoice_created_at: obj.createdAt,
invoice_expires_at: obj.expiresAt,
invoice_memo: obj.memo,

        }

}


    export const FRAGMENT = `
fragment InvoiceFragment on Invoice {
    __typename
    invoice_encoded_invoice: encoded_invoice
    invoice_bitcoin_network: bitcoin_network
    invoice_payment_hash: payment_hash
    invoice_amount: amount {
        __typename
        currency_amount_original_value: original_value
        currency_amount_original_unit: original_unit
        currency_amount_preferred_currency_unit: preferred_currency_unit
        currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded
        currency_amount_preferred_currency_value_approx: preferred_currency_value_approx
    }
    invoice_created_at: created_at
    invoice_expires_at: expires_at
    invoice_memo: memo
}`;




export default Invoice;
