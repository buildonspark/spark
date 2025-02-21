import { useEffect, useState } from "react";

export default function ConfirmQuote({
  sendFiatAmount,
  sendAddress,
  sendAddressNetwork,
}: {
  sendFiatAmount: string;
  sendAddress: string;
  sendAddressNetwork: string;
}) {
  const [btcPrice, setBtcPrice] = useState<number | null>(null);
  useEffect(() => {
    const fetchBtcPrice = async () => {
      try {
        const response = await fetch(
          "https://api.coingecko.com/api/v3/simple/price?ids=bitcoin&vs_currencies=usd"
        );
        const data = await response.json();
        setBtcPrice(data.bitcoin.usd);
      } catch (error) {
        console.error("Error fetching BTC price:", error);
      }
    };

    fetchBtcPrice();
    // Refresh price every minute
    const interval = setInterval(fetchBtcPrice, 60000);

    return () => clearInterval(interval);
  }, []);
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
          {btcPrice
            ? `${(Number(sendFiatAmount) / btcPrice).toFixed(8)} BTC`
            : "0.00000000 BTC"}
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
          $0.00 BTC
        </div>
      </div>
      <div className="flex flex-row font-bold justify-between text-sm/6 mb-5">
        <div className="flex-[0_0_30%]">They'll get</div>
        <div className="flex-[0_0_50%] text-right overflow-hidden text-ellipsis whitespace-nowrap">
          {sendFiatAmount}
        </div>
      </div>
      <div className="flex flex-row font-bold justify-between text-sm/6 mb-5">
        <div className="flex-[0_0_30%]">You'll pay</div>
        <div className="flex-[0_0_50%] text-right overflow-hidden text-ellipsis whitespace-nowrap">
          {sendFiatAmount}
        </div>
      </div>
    </div>
  );
}
