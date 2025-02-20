type CurrencyBalanceDetailsProps = {
  logo?: React.ReactNode;
  currency: string;
  fiatBalance: string;
};

export default function CurrencyBalanceDetails({
  logo,
  currency,
  fiatBalance,
}: CurrencyBalanceDetailsProps) {
  return (
    <div className="flex h-14 px-6 py-2 items-center justify-between gap-2">
      <div className="flex flex-row items-center gap-2">
        <div className="flex w-10 h-10 items-center rounded-xl bg-gradient-to-b from-[#10151C] via-[#11161D] to-[#141A22] border border-[#f9f9f9] border-opacity-10 items-center justify-center">
          {logo}
        </div>
        <div>{currency}</div>
      </div>
      <div>{fiatBalance}</div>
    </div>
  );
}
