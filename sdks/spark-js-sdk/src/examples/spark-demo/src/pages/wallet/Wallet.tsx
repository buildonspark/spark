import { useMemo } from "react";
import { useNavigate } from "react-router-dom";
import Button from "../../components/Button";
import StyledContainer from "../../components/StyledContainer";
import ReceiveIcon from "../../icons/ReceiveIcon";
import SendIcon from "../../icons/SendIcon";
import WalletIcon from "../../icons/WalletIcon";
import { Routes } from "../../routes";
import { useWallet } from "../../store/wallet";
import { getFontSizeForCard, roundDown } from "../../utils/utils";

export enum PrimaryCurrency {
  USD = "USD",
  BTC = "BTC",
}

export default function Wallet() {
  const navigate = useNavigate();

  const { balance: satsBalance, satsUsdPrice } = useWallet();
  // satsBalance.value = 202020;
  console.log(satsBalance);
  let usdBalance = useMemo(() => {
    return satsUsdPrice
      ? roundDown(satsUsdPrice.value * satsBalance.value, 2).toFixed(2)
      : null;
  }, [satsUsdPrice, satsBalance]);

  return (
    <div className="mx-6">
      <div className="flex items-center justify-center gap-2">
        <WalletIcon className="h-[18px] w-[16px]" />
        <div className="font-decimal text-[24px] font-black">Wallet</div>
      </div>
      <StyledContainer className="mt-9 flex h-[180px] w-full items-center justify-center">
        <div>
          <div className="flex max-w-[300px] flex-col justify-center">
            {usdBalance !== null ? (
              <div className="flex font-decimal">
                <div
                  className="max-w-[300px] whitespace-normal break-words text-center"
                  style={{ fontSize: `${getFontSizeForCard(usdBalance)}px` }}
                >
                  {"$" +
                    Number(usdBalance.split(".")[0]).toLocaleString() +
                    "." +
                    usdBalance.split(".")[1]}
                </div>
              </div>
            ) : (
              <div className="flex flex-col items-center justify-center text-center">
                <div
                  className="whitespace-normal break-words leading-[60px]"
                  style={{
                    fontSize: `${getFontSizeForCard(satsBalance.toString())}px`,
                  }}
                >
                  {satsBalance.toLocaleString()}
                </div>
                <div className="text-[13px] opacity-40">SATs</div>
              </div>
            )}
          </div>
          {usdBalance && (
            <div className="flex items-center justify-center font-decimal text-[13px] opacity-40">
              {satsBalance.value} SATs
            </div>
          )}
        </div>
      </StyledContainer>
      <div className="mt-6 flex items-center justify-center gap-4">
        <Button
          text="Send"
          icon={<SendIcon />}
          kind="primary"
          direction="vertical"
          onClick={() => {
            navigate(Routes.Send);
          }}
        />
        <Button
          text="Receive"
          icon={<ReceiveIcon />}
          kind="primary"
          direction="vertical"
          onClick={() => {
            navigate(Routes.Receive);
          }}
        />
      </div>
      {/* <div className="w-full border-y border-[#f9f9f9] border-opacity-10 mt-6">
        <CurrencyBalanceDetails
          logo={<BitcoinIcon strokeWidth="1.50" />}
          currency="Bitcoin"
          fiatBalance="$0.00"
        />
        <CurrencyBalanceDetails
          logo={<StableCoinLogo strokeWidth="1.50" />}
          currency="Stablecoins"
          fiatBalance="$0.00"
        />
      </div> */}
    </div>
  );
}
