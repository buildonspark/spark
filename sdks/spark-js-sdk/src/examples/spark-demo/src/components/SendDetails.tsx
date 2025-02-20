import ArrowUpRight from "../icons/ArrowUpRight";

export default function SendDetails({
  sendAmount,
  sendAddress,
}: {
  sendAmount: string;
  sendAddress: string;
}) {
  return (
    <div className="flex flex-col items-center justify-center">
      <div className="flex h-32 w-32 mt-24 mb-4 bg-[#0E3154] items-center rounded-full justify-center">
        <div className="flex items-center justify-center">
          <ArrowUpRight />
        </div>
      </div>
      <div className="text-[18px] font-normal">Payment sent</div>
      <div className="text-white/50 text-[13px] mt-2">
        ${sendAmount} ({sendAmount} BTC) sent to
      </div>
      <div className="text-white/50 text-[13px]">
        {sendAddress.length > 14
          ? `${sendAddress.slice(0, 7)}...${sendAddress.slice(-6)}`
          : sendAddress}
      </div>
    </div>
  );
}
