import { useWallet } from "../store/wallet";
import { Currency, CurrencyType } from "../utils/currency";

export default function TransactionDetailRow({
  transactionType,
  asset,
  assetAmount,
}: {
  transactionType: "receive" | "send";
  asset: Currency;
  assetAmount: string;
}) {
  const { satsUsdPrice } = useWallet();
  const fiatTransactionAmount =
    asset.type === CurrencyType.TOKEN
      ? (Number(assetAmount) * (asset.usdPrice ?? 1)).toFixed(2)
      : (Number(assetAmount) * satsUsdPrice.value).toFixed(2);
  return (
    <div className="flex flex-row justify-between p-2">
      <div className="text-[13px] font-medium">
        {transactionType === "receive" ? "Received" : "Sent"} {asset.code}
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
          {assetAmount} {asset.code}
        </div>
      </div>
    </div>
  );
}
