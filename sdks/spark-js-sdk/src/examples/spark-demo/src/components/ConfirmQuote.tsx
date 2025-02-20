import { useEffect, useState } from "react";

export default function ConfirmQuote({
  sendAmount,
  sendAddress,
  sendAddressNetwork,
}: {
  sendAmount: string;
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
  const intAmount = sendAmount.split(".")[0];
  const decAmount = sendAmount.split(".")[1];
  const hasDecimal = sendAmount.includes(".");
  return (
    <div>
      <div className="flex flex-col justify-center items-center my-10 rounded-lg h-[200px] bg-[#121E2D] border border-solid border-[rgba(249,249,249,0.12)]">
        <div className="flex font-decimal justify-center">
          <div className="text-[24px] self-center">$</div>
          <div className="text-[60px] leading-[60px]">
            {Number(sendAmount).toLocaleString()}
          </div>
          {(decAmount || hasDecimal) && (
            <div className="text-[24px] self-end">.{decAmount}</div>
          )}
        </div>
        <div className="font-decimal text-[13px] opacity-40 text-center">
          {btcPrice
            ? `${(Number(sendAmount) / btcPrice).toFixed(8)} BTC`
            : "0.00000000 BTC"}
        </div>
      </div>
      <div className="flex flex-row justify-between text-sm/6 mb-5">
        <div>Send to</div>
        <div>{sendAddress}</div>
      </div>
      <div className="flex flex-row justify-between text-sm/6 mb-5">
        <div>Funds arrive</div>
        <div>Instantly</div>
      </div>
      <div className="flex flex-row justify-between text-sm/6 mb-5">
        <div>Your fees</div>
        <div>$0.00 BTC</div>
      </div>
      <div className="flex flex-row font-bold justify-between text-sm/6 mb-5">
        <div>They'll get</div>
        <div>{sendAmount}</div>
      </div>
      <div className="flex flex-row font-bold justify-between text-sm/6 mb-5">
        <div>You'll pay</div>
        <div>{sendAmount}</div>
      </div>
    </div>
  );
}
