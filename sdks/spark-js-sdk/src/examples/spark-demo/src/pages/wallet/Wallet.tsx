import { useMemo } from "react";
import { useNavigate } from "react-router-dom";
import Button from "../../components/Button";
import StyledContainer from "../../components/StyledContainer";
import ReceiveIcon from "../../icons/ReceiveIcon";
import SendIcon from "../../icons/SendIcon";
import WalletIcon from "../../icons/WalletIcon";
import { useWallet } from "../../store/wallet";
import { roundDown } from "../../utils/utils";

export default function Wallet() {
  const navigate = useNavigate();

  const { balance: satsBalance, btcPrice } = useWallet();

  console.log(satsBalance);
  let usdBalance = useMemo(() => {
    return btcPrice
      ? roundDown(btcPrice.value * satsBalance.value, 2).toFixed(2)
      : null;
  }, [btcPrice, satsBalance]);
  const fontSize = Math.max(60 - (satsBalance.toString().length - 1) * 5, 30);
  return (
    <div className="mx-6">
      <div className="flex items-center justify-center gap-2">
        <WalletIcon className="h-[18px] w-[16px]" />
        <div className="font-decimal font-black text-[24px]">Wallet</div>
      </div>
      <StyledContainer className="mt-9 flex items-center justify-center w-full h-[180px]">
        <div>
          <div className="flex flex-col justify-center max-w-[300px]">
            {usdBalance !== null ? (
              <div className="flex font-decimal">
                <div
                  className="text-center break-words whitespace-normal max-w-[300px]"
                  style={{ fontSize: `${fontSize}px` }}
                >
                  {"$" +
                    Number(usdBalance.split(".")[0]).toLocaleString() +
                    "." +
                    usdBalance.split(".")[1]}
                </div>
              </div>
            ) : (
              <div className="flex flex-col justify-center items-center text-center">
                <div
                  className="leading-[60px] break-words whitespace-normal"
                  style={{ fontSize: `${fontSize}px` }}
                >
                  {satsBalance.toLocaleString()}
                </div>
                <div className="text-[13px] opacity-40">SATs</div>
              </div>
            )}
          </div>
          {usdBalance && (
            <div className="flex justify-center items-center font-decimal text-[13px] opacity-40">
              {satsBalance.value} SATs
            </div>
          )}
        </div>
      </StyledContainer>
      <div className="flex items-center justify-center gap-4 mt-6">
        <Button
          text="Send"
          icon={<SendIcon />}
          kind="primary"
          direction="vertical"
          onClick={() => {
            navigate("/send");
          }}
        />
        <Button
          text="Receive"
          icon={<ReceiveIcon />}
          kind="primary"
          direction="vertical"
          onClick={() => {
            navigate("/receive");
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
