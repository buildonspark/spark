import { useMemo } from "react";
import { Currency } from "../utils/currency";
import { getFontSizeForCard, roundDown } from "../utils/utils";

export default function WalletBalance({
  asset,
  assetBalance = 0,
  assetFiatConversion,
}: {
  asset?: Currency;
  assetBalance?: number;
  assetFiatConversion?: number;
}) {
  const usdBalance = useMemo(() => {
    return assetFiatConversion
      ? roundDown(assetFiatConversion * (assetBalance || 0), 2).toFixed(2)
      : null;
  }, [assetFiatConversion, assetBalance]);
  return (
    <div>
      <div className="flex max-w-[300px] flex-col justify-center">
        {usdBalance !== null ? (
          <div className="font-inter flex">
            <div className="relative flex items-end justify-center text-white">
              <div className="flex items-center gap-2">
                <div className="text-xl">$</div>
                <div className="text-6xl">
                  {Number(usdBalance.split(".")[0]).toLocaleString()}
                </div>
              </div>
              <div className="self-end text-xl">
                {usdBalance.split(".")[1] && usdBalance.split(".")[1] !== "00"
                  ? `.${usdBalance.split(".")[1]}`
                  : ""}
              </div>
            </div>
          </div>
        ) : (
          <div className="flex flex-col items-center justify-center text-center">
            <div
              className="whitespace-normal break-words leading-[60px]"
              style={{
                fontSize: `${getFontSizeForCard(assetBalance.toString())}px`,
              }}
            >
              {assetBalance.toLocaleString()}
            </div>
            <div className="text-[13px] opacity-40">
              {asset?.code === "BTC" ? "SATs" : asset?.code}
            </div>
          </div>
        )}
      </div>
      {usdBalance && (
        <div className="font-inter flex items-center justify-center text-[13px] opacity-40">
          {assetBalance} {asset?.code === "BTC" ? "SATs" : asset?.code}
        </div>
      )}
    </div>
  );
}
