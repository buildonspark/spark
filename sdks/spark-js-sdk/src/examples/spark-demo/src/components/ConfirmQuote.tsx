import { decode } from "light-bolt11-decoder";
import { useWallet } from "../store/wallet";
import { CurrencyType } from "../utils/currency";
import { Network } from "./Networks";

const decodeLnInvoiceSafely = (invoice: string) => {
  try {
    return decode(invoice);
  } catch (error) {
    console.error(error);
    return null;
  }
};

export default function ConfirmQuote({
  inputAmount,
  sendAddress,
  sendAddressNetwork,
}: {
  inputAmount: string;
  sendAddress: string;
  sendAddressNetwork: string;
}) {
  const { activeAsset, satsUsdPrice, activeInputCurrency } = useWallet();
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
        : `${(Number(inputAmount) * (activeAsset.type === CurrencyType.TOKEN ? (activeAsset.usdPrice ?? 1) : satsUsdPrice.value)).toFixed(2)}`;
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
    <div>
      <div
        className="bg-opacity-4 mb-10 flex h-[200px] flex-col items-center justify-center rounded-lg bg-[white]"
        style={{
          border: "0.5px solid rgba(249, 249, 249, 0.1)",
        }}
      >
        <div className="flex justify-center font-decimal">
          <div className="self-center text-[24px]">$</div>
          <div className="text-[60px] leading-[60px]">
            {Number(sendFiatAmount.split(".")[0]).toLocaleString()}
          </div>
          {sendFiatAmount.split(".")[1] &&
            sendFiatAmount.split(".")[1] !== "00" && (
              <div className="self-end text-[24px]">
                .{sendFiatAmount.split(".")[1]}
              </div>
            )}
        </div>
        <div className="text-center font-decimal text-[13px] opacity-40">
          {Number(sendAssetAmount).toLocaleString()}{" "}
          {activeAsset.code === "BTC" ? "SATs" : activeAsset.code}
        </div>
      </div>

      <div className="mb-5 flex flex-row justify-between text-sm/6">
        <div className="flex-[0_0_30%]">Send to</div>
        <div className="flex-[0_0_50%] overflow-hidden text-ellipsis whitespace-nowrap text-right">
          {sendAddressNetwork === Network.PHONE
            ? `${sendAddress.slice(0, 2)} (${sendAddress.slice(
                2,
                5,
              )}) ${sendAddress.slice(5, 8)}-${sendAddress.slice(8, 12)}`
            : sendAddress}
        </div>
      </div>
      <div className="mb-5 flex flex-row justify-between text-sm/6">
        <div className="flex-[0_0_30%]">Funds arrive</div>
        <div className="flex-[0_0_50%] overflow-hidden text-ellipsis whitespace-nowrap text-right">
          Instantly
        </div>
      </div>
      <div className="mb-5 flex flex-row justify-between text-sm/6">
        <div className="flex-[0_0_30%]">Your fees</div>
        <div className="flex-[0_0_50%] overflow-hidden text-ellipsis whitespace-nowrap text-right">
          $0.00
        </div>
      </div>
      <div className="mb-5 flex flex-row justify-between text-sm/6 font-bold">
        <div className="flex-[0_0_30%]">They'll get</div>
        <div className="flex-[0_0_50%] overflow-hidden text-ellipsis whitespace-nowrap text-right">
          {"$" + Number(sendFiatAmount).toLocaleString()}
        </div>
      </div>
      <div className="mb-5 flex flex-row justify-between text-sm/6 font-bold">
        <div className="flex-[0_0_30%]">You'll pay</div>
        <div className="flex-[0_0_50%] overflow-hidden text-ellipsis whitespace-nowrap text-right">
          {"$" + Number(sendFiatAmount).toLocaleString()}
        </div>
      </div>
    </div>
  );
}
