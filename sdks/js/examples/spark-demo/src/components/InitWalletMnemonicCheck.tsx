export default function InitWalletMnemonicCheck({
  mnemonicCheckIdx,
  setMnemonicCheckInputWord,
  mnemonicCheckSuccess,
}: {
  mnemonicCheckIdx: number;
  setMnemonicCheckInputWord: (word: string) => void;
  mnemonicCheckSuccess: boolean | undefined;
}) {
  return (
    <>
      <div>
        <h2 className="text-[20px] font-[600] leading-[25px]">
          Enter your phrase
        </h2>
        <p className="mt-sm flex w-full flex-col text-[15px] font-[500] leading-[20px]">
          <span className="w-full text-white-50">
            Confirm these words in your Recovery Phrase.
          </span>
        </p>
      </div>
      <input
        type="text"
        className={`mt-xl w-full rounded-md border ${mnemonicCheckSuccess === false ? "border-red" : "border-white-24"} bg-black px-xl py-md text-[15px] font-[400] leading-[18px] text-white-50`}
        placeholder={`Word ${mnemonicCheckIdx + 1}`}
        onChange={(e) => {
          setMnemonicCheckInputWord(e.target.value);
        }}
      />
      {mnemonicCheckSuccess === false && (
        <span className="ml-2 mt-sm text-[13px] text-red">Incorrect word</span>
      )}
    </>
  );
}
