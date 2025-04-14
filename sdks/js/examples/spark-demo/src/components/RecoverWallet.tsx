export default function RecoverWallet({
  mnemonic,
  setMnemonic,
}: {
  mnemonic: string | undefined;
  setMnemonic: (mnemonic: string) => void;
}) {
  return (
    <>
      <div>
        <h2 className="text-[20px] font-[600] leading-[25px]">
          Recover your wallet
        </h2>
        <p className="mt-sm flex w-full flex-col text-[15px] font-[500] leading-[20px]">
          <span className="w-full text-white-50">
            Paste your Recovery Phrase to import your wallet back to Spark.
          </span>
        </p>
      </div>
      <div className="relative mt-xl flex h-21xl w-full flex-wrap justify-center gap-x-8 rounded-xl bg-white-6 px-3xl py-xl text-[13px] font-[400] leading-[18px]">
        {mnemonic !== undefined ? (
          mnemonic?.split(" ").map((word, idx) => (
            <div
              className="flex w-5/12 items-center py-2 text-left"
              key={`${word}-${idx}`}
            >
              <span className="mr-2 text-white-50">{`${idx + 1}. `}</span>
              <span className="text-white">{word}</span>
            </div>
          ))
        ) : (
          <>
            {new Array(12).fill(0).map((_, idx) => (
              <div
                className="flex w-5/12 items-center py-2 text-left"
                key={`${idx}`}
              >
                <span className="mr-2 text-white-50">{`${idx + 1}. `}</span>
                <div className="h-[18px] w-[75px] rounded-full bg-white-4"></div>
              </div>
            ))}
            <button
              className="absolute left-1/2 top-1/2 z-10 h-[44px] w-[190px] -translate-x-1/2 -translate-y-1/2 transform rounded-xl bg-white px-4 text-[15px] font-[500] text-black"
              onClick={() => {
                navigator.clipboard.readText().then((text) => {
                  setMnemonic(text);
                });
              }}
            >
              Paste from clipboard
            </button>
          </>
        )}
      </div>
    </>
  );
}
