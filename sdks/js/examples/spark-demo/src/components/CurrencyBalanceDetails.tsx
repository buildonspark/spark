import ChevronIcon from "../icons/ChevronIcon";

type CurrencyBalanceDetailsProps = {
  logo?: React.ReactNode;
  currency: string;
  fiatBalance: string;
  onClick?: () => void;
  logoBorderEnabled?: boolean;
};

export default function CurrencyBalanceDetails({
  logo,
  currency,
  fiatBalance,
  logoBorderEnabled = true,
  onClick,
}: CurrencyBalanceDetailsProps) {
  return (
    <div
      className={`flex h-16 cursor-pointer items-center justify-between px-2 py-2 ${
        onClick ? "cursor-pointer" : ""
      }`}
      onClick={onClick}
    >
      <div className="flex flex-row items-center gap-2">
        {logo && (
          <div
            className={`flex h-10 w-10 items-center justify-center rounded-xl ${
              logoBorderEnabled
                ? "border border-[#f9f9f9] border-opacity-10 bg-gradient-to-b from-[#10151C] via-[#11161D] to-[#141A22]"
                : ""
            }`}
          >
            {logo}
          </div>
        )}
        <div
          className={`max-w-[150px] overflow-hidden text-ellipsis whitespace-nowrap text-xs font-medium ${
            !logo && "ml-4"
          }`}
        >
          {currency}
        </div>
      </div>
      {/* <div>{fiatBalance}</div> */}
      <div>
        <ChevronIcon direction="right" />
      </div>
    </div>
  );
}
