import { useWallet } from "../store/wallet";
import { Currency, CurrencyType } from "../utils/currency";

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
  const fiatTransactionAmount =
    asset.type === CurrencyType.TOKEN
      ? (assetAmount * (asset.usdPrice ?? 1)).toFixed(2)
      : (assetAmount * satsUsdPrice.value).toFixed(2);
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
            transactionType === "receive" ? "text-green-500" : ""
          }`}
        >
          {transactionType === "receive" ? "+" : ""}$
          {fiatTransactionAmount}{" "}
        </div>
        <div className="text-[11px] text-[#F9F9F999]">
          {assetAmount} {asset.code === "BTC" ? "SATs" : asset.code}
        </div>
      </div>
    </div>
  );
}
