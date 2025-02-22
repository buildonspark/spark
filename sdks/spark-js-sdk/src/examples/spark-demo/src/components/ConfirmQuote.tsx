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
      <div className="flex flex-col justify-center items-center my-10 rounded-lg h-[200px] bg-[#121E2D] border border-solid border-[rgba(249,249,249,0.12)]">
        <div className="flex font-decimal justify-center">
          <div className="text-[24px] self-center">$</div>
          <div className="text-[60px] leading-[60px]">
            {Number(sendFiatAmount).toLocaleString()}
          </div>
          {(decAmount || hasDecimal) && (
            <div className="text-[24px] self-end">.{decAmount}</div>
          )}
        </div>
        <div className="font-decimal text-[13px] opacity-40 text-center">
          {satsUsdPrice
            ? `${(Number(sendFiatAmount) / satsUsdPrice.value).toFixed(0)} SATs`
            : "000000000 SATs"}
        </div>
      </div>

      <div className="flex flex-row justify-between text-sm/6 mb-5">
        <div className="flex-[0_0_30%]">Send to</div>
        <div className="flex-[0_0_50%] text-right overflow-hidden text-ellipsis whitespace-nowrap">
          {sendAddress}
        </div>
      </div>
      <div className="flex flex-row justify-between text-sm/6 mb-5">
        <div className="flex-[0_0_30%]">Funds arrive</div>
        <div className="flex-[0_0_50%] text-right overflow-hidden text-ellipsis whitespace-nowrap">
          Instantly
        </div>
      </div>
      <div className="flex flex-row justify-between text-sm/6 mb-5">
        <div className="flex-[0_0_30%]">Your fees</div>
        <div className="flex-[0_0_50%] text-right overflow-hidden text-ellipsis whitespace-nowrap">
          $0.00
        </div>
      </div>
      <div className="flex flex-row font-bold justify-between text-sm/6 mb-5">
        <div className="flex-[0_0_30%]">They'll get</div>
        <div className="flex-[0_0_50%] text-right overflow-hidden text-ellipsis whitespace-nowrap">
          {"$" + Number(sendFiatAmount).toLocaleString()}
        </div>
      </div>
      <div className="flex flex-row font-bold justify-between text-sm/6 mb-5">
        <div className="flex-[0_0_30%]">You'll pay</div>
        <div className="flex-[0_0_50%] text-right overflow-hidden text-ellipsis whitespace-nowrap">
          {"$" + Number(sendFiatAmount).toLocaleString()}
        </div>
      </div>
    </div>
  );
}
