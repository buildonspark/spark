import { useWallet } from "../store/wallet";

export default function ConfirmQuote({
  sendFiatAmount,
  sendAddress,
  sendAddressNetwork,
}: {
  sendFiatAmount: string;
  sendAddress: string;
  sendAddressNetwork: string;
}) {
  console.log("sendFiatAmount", sendFiatAmount);
  const { satsUsdPrice } = useWallet();
  const intAmount = sendFiatAmount.split(".")[0];
  const decAmount = sendFiatAmount.split(".")[1];
  const hasDecimal = sendFiatAmount.includes(".");
  return (
    <div>
      <div className="my-10 flex h-[200px] flex-col items-center justify-center rounded-lg border border-solid border-[rgba(249,249,249,0.12)] bg-[#121E2D]">
        <div className="flex justify-center font-decimal">
          <div className="self-center text-[24px]">$</div>
          <div className="text-[60px] leading-[60px]">
            {Number(intAmount).toLocaleString()}
          </div>
          {(decAmount || hasDecimal) && (
            <div className="self-end text-[24px]">.{decAmount}</div>
          )}
        </div>
        <div className="text-center font-decimal text-[13px] opacity-40">
          {satsUsdPrice
            ? `${(Number(sendFiatAmount) / satsUsdPrice.value).toFixed(0)} SATs`
            : "000000000 SATs"}
        </div>
      </div>

      <div className="mb-5 flex flex-row justify-between text-sm/6">
        <div className="flex-[0_0_30%]">Send to</div>
        <div className="flex-[0_0_50%] overflow-hidden text-ellipsis whitespace-nowrap text-right">
          {sendAddress}
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
