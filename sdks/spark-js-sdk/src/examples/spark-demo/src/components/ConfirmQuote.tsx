import { useWallet } from "../store/wallet";
import { CurrencyType } from "../utils/currency";
import { Network } from "./Networks";

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
  const sendFiatAmount =
    activeInputCurrency.type === CurrencyType.FIAT
      ? inputAmount
      : `${(Number(inputAmount) * satsUsdPrice.value).toFixed(2)}`;
  const sendAssetAmount =
    activeInputCurrency.type === CurrencyType.FIAT
      ? (Number(inputAmount) / satsUsdPrice.value).toFixed(0)
      : inputAmount;
  return (
    <div>
      <div
        className="mb-10 flex h-[200px] flex-col items-center justify-center rounded-lg"
        style={{
          border: "0.5px solid rgba(249, 249, 249, 0.1)",
          background:
            "linear-gradient(180deg, #141A22 0%, #141A22 11.79%, #131A22 21.38%, #131922 29.12%, #131922 35.34%, #131921 40.37%, #131921 44.56%, #121820 48.24%, #121820 51.76%, #12171F 55.44%, #11171F 59.63%, #11171E 64.66%, #11161E 70.88%, #11161D 78.62%, #10151C 88.21%, #10151C 100%)",
          boxShadow:
            "0px 216px 60px 0px rgba(0, 0, 0, 0.00), 0px 138px 55px 0px rgba(0, 0, 0, 0.01), 0px 78px 47px 0px rgba(0, 0, 0, 0.05), 0px 35px 35px 0px rgba(0, 0, 0, 0.09), 0px 9px 19px 0px rgba(0, 0, 0, 0.10)",
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
