import ChevronRightIcon from "../icons/ChevronRightIcon";

type CurrencyBalanceDetailsProps = {
  logo?: React.ReactNode;
  currency: string;
  fiatBalance: string;
  onClick?: () => void;
};

export default function CurrencyBalanceDetails({
  logo,
  currency,
  fiatBalance,
  onClick,
}: CurrencyBalanceDetailsProps) {
  return (
    <div
      className="flex h-16 items-center justify-between px-2 py-2"
      onClick={onClick}
    >
      <div className="flex flex-row items-center gap-2">
        <div className="flex h-10 w-10 items-center justify-center rounded-xl border border-[#f9f9f9] border-opacity-10 bg-gradient-to-b from-[#10151C] via-[#11161D] to-[#141A22]">
          {logo}
        </div>
        <div>{currency}</div>
      </div>
      {/* <div>{fiatBalance}</div> */}
      <div>
        <ChevronRightIcon />
      </div>
    </div>
  );
}
