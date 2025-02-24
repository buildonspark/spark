import LightsparkClient from "../client.js";
import CurrencyAmount from "./CurrencyAmount.js";
import SparkTransferToLeavesConnection from "./SparkTransferToLeavesConnection.js";
declare class Transfer {
    /** The total amount of the transfer. **/
    readonly totalAmount: CurrencyAmount;
    /** The id of the transfer known at signing operators. If not set, the transfer hasn't been
     * initialized. **/
    readonly sparkId?: string | undefined;
    constructor(
    /** The total amount of the transfer. **/
    totalAmount: CurrencyAmount, 
    /** The id of the transfer known at signing operators. If not set, the transfer hasn't been
     * initialized. **/
    sparkId?: string | undefined);
    getLeaves(client: LightsparkClient, first?: number | undefined, after?: string | undefined): Promise<SparkTransferToLeavesConnection>;
    toJson(): {
        transfer_total_amount: any;
        transfer_spark_id: string | undefined;
    };
}
export declare const TransferFromJson: (obj: any) => Transfer;
export declare const FRAGMENT = "\nfragment TransferFragment on Transfer {\n    __typename\n    transfer_total_amount: total_amount {\n        __typename\n        currency_amount_original_value: original_value\n        currency_amount_original_unit: original_unit\n        currency_amount_preferred_currency_unit: preferred_currency_unit\n        currency_amount_preferred_currency_value_rounded: preferred_currency_value_rounded\n        currency_amount_preferred_currency_value_approx: preferred_currency_value_approx\n    }\n    transfer_spark_id: spark_id\n}";
export default Transfer;
