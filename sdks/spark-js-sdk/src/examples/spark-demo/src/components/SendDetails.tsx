import ArrowUpRight from "../icons/ArrowUpRight";
import { useWallet } from "../store/wallet";
export default function SendDetails({
  sendFiatAmount,
  sendAddress,
}: {
  sendFiatAmount: string;
  sendAddress: string;
}) {
  const { satsUsdPrice } = useWallet();
  return (
    <div className="flex flex-col items-center justify-center">
      <div className="mb-4 mt-24 flex h-32 w-32 items-center justify-center rounded-full bg-[#0E3154]">
        <div className="flex items-center justify-center">
          <ArrowUpRight />
        </div>
      </div>
      <div className="text-[18px] font-normal">Payment sent</div>
      <div className="mt-2 text-[13px] text-white/50">
        ${sendFiatAmount} ({" "}
        {satsUsdPrice && Number(sendFiatAmount) / satsUsdPrice} SATs sent to)
      </div>
      <div className="text-[13px] text-white/50">
        {sendAddress.length > 14
          ? `${sendAddress.slice(0, 7)}...${sendAddress.slice(-6)}`
          : sendAddress}
      </div>
    </div>
  );
}
