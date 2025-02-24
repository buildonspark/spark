import ArrowUpRight from "../icons/ArrowUpRight";
import { useWallet } from "../store/wallet";
import { CurrencyType } from "../utils/currency";
export default function SendDetails({
  inputAmount,
  sendAddress,
}: {
  inputAmount: string;
  sendAddress: string;
}) {
  const { satsUsdPrice, activeAsset, activeInputCurrency } = useWallet();
  const sendFiatAmount =
    activeInputCurrency.type === CurrencyType.FIAT
      ? inputAmount
      : (Number(inputAmount) * satsUsdPrice.value).toFixed(2);
  const sendAssetAmount =
    activeInputCurrency.type === CurrencyType.FIAT
      ? (Number(inputAmount) / satsUsdPrice.value).toFixed(0)
      : inputAmount;
  return (
    <div className="mb-10 mt-4 flex flex-col items-center justify-center">
      <div className="mb-4 mt-4 flex h-32 w-32 items-center justify-center rounded-full bg-[#0E3154]">
        <div className="flex items-center justify-center">
          <ArrowUpRight />
        </div>
      </div>
      <div className="text-[18px] font-normal">Payment sent</div>
      <div className="mt-2 text-[13px] text-white/50">
        ${Number(sendFiatAmount.split(".")[0]).toLocaleString()}
        {sendFiatAmount.split(".")[1] && `.${sendFiatAmount.split(".")[1]}`} (
        {Number(sendAssetAmount).toLocaleString()}{" "}
        {activeAsset.code === "BTC" ? "SATs" : activeAsset.code || ""}) sent to
      </div>
      <div className="text-[13px] text-white/50">
        {sendAddress.length > 14
          ? `${sendAddress.slice(0, 7)}...${sendAddress.slice(-6)}`
          : sendAddress}
      </div>
    </div>
  );
}
