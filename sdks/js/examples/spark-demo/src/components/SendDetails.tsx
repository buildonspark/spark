import ArrowUpRight from "../icons/ArrowUpRight";
import WarningIcon from "../icons/WarningIcon";
import { useWallet } from "../store/wallet";
import { CurrencyType } from "../utils/currency";
import { decodeLnInvoiceSafely } from "../utils/utils";
import { Network } from "./Networks";

export default function SendDetails({
  inputAmount,
  sendAddress,
  sendAddressNetwork,
  success,
}: {
  inputAmount: string;
  sendAddress: string;
  sendAddressNetwork?: string;
  success?: boolean;
}) {
  const { satsUsdPrice, activeAsset, activeInputCurrency } = useWallet();
  const decodedLnInvoice = decodeLnInvoiceSafely(sendAddress) || null;
  const decodedLnSatsAmount: number =
    Number(
      decodedLnInvoice?.sections.find((section) => section.name === "amount")
        ?.value,
    ) / 1000 || 0;
  const sendFiatAmount =
    sendAddressNetwork === Network.LIGHTNING
      ? `${(decodedLnSatsAmount * satsUsdPrice.value).toFixed(2)}`
      : activeInputCurrency.type === CurrencyType.FIAT
        ? inputAmount
        : (
            Number(inputAmount) *
            (activeAsset.type === CurrencyType.TOKEN
              ? (activeAsset.usdPrice ?? 1)
              : satsUsdPrice.value)
          ).toFixed(2);
  const sendAssetAmount =
    sendAddressNetwork === Network.LIGHTNING
      ? decodedLnSatsAmount
      : activeInputCurrency.type === CurrencyType.FIAT
        ? (
            Number(inputAmount) /
            (activeAsset.type === CurrencyType.TOKEN
              ? (activeAsset.usdPrice ?? 1)
              : satsUsdPrice.value)
          ).toFixed(0)
        : inputAmount;
  return (
    <div className="mb-10 mt-4 flex flex-col items-center justify-center">
      <div className="mb-4 mt-4 flex h-32 w-32 items-center justify-center rounded-full bg-[white] bg-opacity-4">
        <div className="flex items-center justify-center">
          {success ? <ArrowUpRight /> : <WarningIcon />}
        </div>
      </div>
      {success ? (
        <>
          <div className="text-[18px] font-normal">Payment submitted</div>
          <div className="mt-2 text-[13px] text-white/50">
            ${Number(sendFiatAmount.split(".")[0]).toLocaleString()}
            {sendFiatAmount.split(".")[1] &&
              `.${sendFiatAmount.split(".")[1]}`}{" "}
            ({Number(sendAssetAmount).toLocaleString()}{" "}
            {activeAsset.code === "BTC" ? "SATs" : activeAsset.code || ""}) sent
            to
          </div>
          <div className="text-[13px] text-white/50">
            {sendAddress.length > 14
              ? `${sendAddress.slice(0, 7)}...${sendAddress.slice(-6)}`
              : sendAddress}
          </div>
        </>
      ) : (
        <>
          <div className="mb-2 text-[18px] font-normal">
            Sorry, we had an issue
          </div>
          <div className="text-center text-[13px] text-white/50">
            <div>We had a problem with sending your payment.</div>
            <div>Please try again.</div>
          </div>
        </>
      )}
    </div>
  );
}
