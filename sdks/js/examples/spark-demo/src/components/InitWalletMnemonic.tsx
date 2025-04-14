export default function InitWalletMnemonic({
  mnemonic,
}: {
  mnemonic: string | undefined;
}) {
  return (
    <>
      <div>
        <h2 className="text-[20px] font-[600] leading-[25px]">
          Your Recovery Phrase
        </h2>
        <p className="mt-sm flex w-full flex-col text-[15px] font-[500] leading-[20px]">
          <span className="w-full text-white-50">
            This has access to everything in your wallet.
          </span>
          <span className="w-full text-white-50">Keep it safe.</span>
        </p>
      </div>
      <div className="mt-xl flex h-21xl w-full flex-wrap justify-center gap-x-8 rounded-xl bg-white-6 px-3xl py-xl text-[13px] font-[400] leading-[18px]">
        {mnemonic?.split(" ").map((word, idx) => (
          <div
            className="flex w-5/12 items-center py-2 text-left"
            key={`${word}-${idx}`}
          >
            <span className="mr-2 text-white-50">{`${idx + 1}. `}</span>
            <span className="text-white">{word}</span>
          </div>
        ))}
      </div>
    </>
  );
}
