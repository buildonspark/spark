import { useEffect } from "react";
import styled from "styled-components";
import DeleteIcon from "../icons/DeleteIcon";
import ToggleIcon from "../icons/ToggleIcon";
import { useWallet } from "../store/wallet";
export default function AmountInput({
  fiatAmount,
  setFiatAmount,
  togglePrimaryCurrency,
}: {
  fiatAmount: string;
  setFiatAmount: React.Dispatch<React.SetStateAction<string>>;
  togglePrimaryCurrency: () => void;
}) {
  const { btcPrice } = useWallet();

  const handleKey = (key: string) => {
    setFiatAmount((prev) => {
      if (key === "Backspace") {
        if (prev.length === 1) {
          return "0";
        }

        return prev.slice(0, -1);
      }

      if (!isNaN(Number(prev + key))) {
        if (prev === "0" && key !== ".") {
          return key;
        }

        if (prev.length >= 4 && prev[prev.length - 3] === ".") {
          return prev;
        }
        return prev + key;
      }
      return prev;
    });
  };

  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      handleKey(e.key);
    };

    window.addEventListener("keydown", handleKeyDown);

    return () => {
      window.removeEventListener("keydown", handleKeyDown);
    };
  }, [fiatAmount]);

  const intAmount = fiatAmount.split(".")[0];
  const decAmount = fiatAmount.split(".")[1];
  const hasDecimal = fiatAmount.includes(".");
  return (
    <div className="flex flex-col gap-2 items-center w-full">
      <div className="my-10">
        <div className="flex  font-decimal justify-center">
          <div className="text-[24px] self-center">$</div>
          <div className="text-[60px] leading-[60px]">
            {Number(intAmount).toLocaleString()}
          </div>
          {(decAmount || hasDecimal) && (
            <div className="text-[24px] self-end">.{decAmount}</div>
          )}
        </div>
        <div
          className="font-decimal text-[13px] opacity-40 text-center flex items-center gap-2 rounded-full bg-[#F9F9F9] bg-opacity-20 px-2 py-1"
          onClick={togglePrimaryCurrency}
        >
          {btcPrice
            ? `${Math.floor(Number(fiatAmount) / btcPrice.value).toFixed(
                0
              )} SATs`
            : "0 SATs"}
          <ToggleIcon />
        </div>
      </div>
      <div className="flex flex-col gap-2 font-decimal text-[22px]">
        <div className="flex gap-2">
          <AmountInputButton onClick={() => handleKey("1")}>
            1
          </AmountInputButton>
          <AmountInputButton onClick={() => handleKey("2")}>
            2
          </AmountInputButton>
          <AmountInputButton onClick={() => handleKey("3")}>
            3
          </AmountInputButton>
        </div>
        <div className="flex gap-2">
          <AmountInputButton onClick={() => handleKey("4")}>
            4
          </AmountInputButton>
          <AmountInputButton onClick={() => handleKey("5")}>
            5
          </AmountInputButton>
          <AmountInputButton onClick={() => handleKey("6")}>
            6
          </AmountInputButton>
        </div>
        <div className="flex gap-2">
          <AmountInputButton onClick={() => handleKey("7")}>
            7
          </AmountInputButton>
          <AmountInputButton onClick={() => handleKey("8")}>
            8
          </AmountInputButton>
          <AmountInputButton onClick={() => handleKey("9")}>
            9
          </AmountInputButton>
        </div>
        <div className="flex gap-2">
          <AmountInputButton onClick={() => handleKey(".")}>
            .
          </AmountInputButton>
          <AmountInputButton onClick={() => handleKey("0")}>
            0
          </AmountInputButton>
          <AmountInputButton onClick={() => handleKey("Backspace")}>
            <DeleteIcon />
          </AmountInputButton>
        </div>
      </div>
    </div>
  );
}

const AmountInputButton = styled.button`
  width: 80px;
  height: 80px;
  display: flex;
  align-items: center;
  justify-content: center;
  outline: none;
`;
