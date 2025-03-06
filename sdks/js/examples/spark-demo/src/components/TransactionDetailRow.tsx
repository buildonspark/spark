import { PERMANENT_CURRENCIES, useWallet } from "../store/wallet";
import { Currency, CurrencyType } from "../utils/currency";
import {
  formatAssetAmountDisplayString,
  formatFiatAmountDisplayString,
} from "../utils/utils";

export default function TransactionDetailRow({
  transactionType,
  asset,
  assetAmount,
  counterparty,
}: {
  transactionType: "receive" | "send";
  asset: Currency;
  assetAmount: number;
  counterparty: string;
}) {
  const { satsUsdPrice } = useWallet();

  return (
    <div className="flex flex-row justify-between p-2">
      <div className="max-w-[100px] text-[13px] font-medium">
        {transactionType === "receive" ? "Received" : "Sent"} {asset.code}
        <div className="overflow-hidden text-ellipsis whitespace-nowrap text-[11px] text-[#F9F9F999]">
          {transactionType === "receive"
            ? ` From ${counterparty}`
            : ` To ${counterparty}`}
        </div>
      </div>
      <div className="flex flex-col items-end">
        <div
          className={`text-[13px] font-medium ${
            transactionType === "receive" ? "text-green" : ""
          }`}
        >
          {formatFiatAmountDisplayString(
            assetAmount,
            asset.type === CurrencyType.TOKEN
              ? (asset.usdPrice ?? 1)
              : satsUsdPrice.value,
            PERMANENT_CURRENCIES.get("USD")!,
            true,
          )}
        </div>
        <div className="text-[11px] text-[#F9F9F999]">
          {formatAssetAmountDisplayString(assetAmount, asset, true)}
        </div>
      </div>
    </div>
  );
}
