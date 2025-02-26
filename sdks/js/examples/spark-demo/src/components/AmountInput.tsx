import { useCallback, useEffect } from "react";
import styled from "styled-components";
import DeleteIcon from "../icons/DeleteIcon";
import ToggleIcon from "../icons/ToggleIcon";

import { PERMANENT_CURRENCIES, useWallet } from "../store/wallet";
import { CurrencyType } from "../utils/currency";

const FiatAmountPrimaryDisplay = ({
  parsedString,
}: {
  parsedString: string;
}) => {
  const intAmount = parsedString.split(".")[0];
  const decAmount = parsedString.split(".")[1];
  return (
    <div className="relative flex items-end justify-center text-white">
      <div className="flex items-center gap-2">
        <div className="text-xl">$</div>
        <div className="text-6xl">{intAmount}</div>
      </div>
      <div className="self-end text-xl">
        {decAmount && decAmount !== "00" ? `.${decAmount}` : ""}
      </div>
    </div>
  );
};

const AssetAmountPrimaryDisplay = ({
  parsedString,
}: {
  parsedString: string;
}) => {
  const { activeAsset } = useWallet();
  return (
    <div className="text-6xl">
      {Number(parsedString).toLocaleString()}
      <span className="text-sm">
        {activeAsset.code === "BTC" ? "SATs" : activeAsset.code}
      </span>
    </div>
  );
};

export default function AmountInput({
  rawInputAmount,
  setRawInputAmount,
}: {
  rawInputAmount: string;
  setRawInputAmount: React.Dispatch<React.SetStateAction<string>>;
}) {
  const {
    satsUsdPrice,
    activeInputCurrency,
    setActiveInputCurrency,
    activeAsset,
  } = useWallet();

  const handleKey = useCallback(
    (key: string) => {
      setRawInputAmount((prev) => {
        if (key === "Backspace") {
          if (prev.length === 1) {
            return "0";
          }
          return prev.slice(0, -1);
        }

        // Check if the key is a decimal point and the active currency is BTC
        if (key === "." && activeInputCurrency.type !== CurrencyType.FIAT) {
          return prev; // Ignore the decimal point in sats mode
        }

        if (!isNaN(Number(prev + key))) {
          if (prev === "0" && key !== ".") {
            return key;
          }
          const decimalIndex = prev.indexOf(".");
          if (decimalIndex !== -1 && prev.length - decimalIndex > 2) {
            // If there are already two decimal places, prevent further input
            return prev;
          }
          return prev + key;
        }
        return prev;
      });
    },
    [activeInputCurrency, setRawInputAmount],
  );

  useEffect(() => {
    const handleKeyDown = (e: KeyboardEvent) => {
      handleKey(e.key);
    };

    window.addEventListener("keydown", handleKeyDown);

    return () => {
      window.removeEventListener("keydown", handleKeyDown);
    };
  }, [handleKey]);

  const resolveCurrencyDisplay = useCallback(() => {
    const intAmount = rawInputAmount.split(".")[0];
    const decAmount = rawInputAmount.split(".")[1];

    const fiatAmountString =
      activeInputCurrency.type === CurrencyType.FIAT
        ? `${Number(intAmount).toLocaleString()}${
            decAmount ? `.${decAmount}` : ""
          }`
        : (
            Number(rawInputAmount) *
            (activeAsset.type === CurrencyType.TOKEN
              ? (activeAsset.usdPrice ?? 1)
              : satsUsdPrice.value)
          ).toFixed(2);
    const assetAmountString =
      activeInputCurrency.type !== CurrencyType.FIAT
        ? `${rawInputAmount}`
        : Number(
            (
              Number(rawInputAmount) /
              (activeAsset.type === CurrencyType.TOKEN
                ? (activeAsset.usdPrice ?? 1)
                : satsUsdPrice.value)
            ).toFixed(0),
          ).toLocaleString();
    return {
      fiatAmountString,
      assetAmountString,
    };
  }, [satsUsdPrice, rawInputAmount, activeInputCurrency]);

  useEffect(() => {
    resolveCurrencyDisplay();
  }, [
    rawInputAmount,
    satsUsdPrice,
    resolveCurrencyDisplay,
    activeInputCurrency,
  ]);

  return (
    <div className="flex w-full flex-col items-center gap-2">
      <div className="my-10">
        <div className="flex justify-center font-decimal text-[60px] leading-[60px]">
          {activeInputCurrency.type === CurrencyType.FIAT ? (
            rawInputAmount ? (
              <FiatAmountPrimaryDisplay
                parsedString={resolveCurrencyDisplay().fiatAmountString}
              />
            ) : (
              <FiatAmountPrimaryDisplay parsedString={"0"} />
            )
          ) : rawInputAmount ? (
            <AssetAmountPrimaryDisplay
              parsedString={resolveCurrencyDisplay().assetAmountString}
            />
          ) : (
            <AssetAmountPrimaryDisplay parsedString={"0"} />
          )}
        </div>
        <div className="flex items-center justify-center gap-2">
          <div
            className="mt-2 flex inline-flex items-center gap-2 rounded-full bg-[#F9F9F9] bg-opacity-20 px-2 py-1 text-center font-decimal text-[13px] opacity-40 active:bg-opacity-40"
            onClick={() => {
              const { fiatAmountString, assetAmountString } =
                resolveCurrencyDisplay();
              if (activeInputCurrency.type !== CurrencyType.FIAT) {
                const removeCommas = fiatAmountString.replace(/,/g, ""); // remove commas
                const cleanedInput = removeCommas.replace(/\.00$/, ""); // remove trailing .00
                setRawInputAmount(cleanedInput);
                setActiveInputCurrency(PERMANENT_CURRENCIES.USD);
              } else {
                const parsedInput = assetAmountString.replace(/,/g, ""); // remove commas
                setRawInputAmount(parsedInput.length > 0 ? parsedInput : "0");
                setActiveInputCurrency(activeAsset);
              }
            }}
          >
            {activeInputCurrency.type === CurrencyType.FIAT
              ? rawInputAmount
                ? `${resolveCurrencyDisplay().assetAmountString} ${activeAsset.code === "BTC" ? "SATs" : activeAsset.code}`
                : `0 ${activeAsset.code === "BTC" ? "SATs" : activeAsset.code}`
              : rawInputAmount
                ? `$${resolveCurrencyDisplay().fiatAmountString}`
                : "$0"}
            <ToggleIcon />
          </div>
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
