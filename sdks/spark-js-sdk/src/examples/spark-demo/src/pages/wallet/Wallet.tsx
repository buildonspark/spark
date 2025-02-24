import { useNavigate } from "react-router-dom";
import Button from "../../components/Button";
import CurrencyBalanceDetails from "../../components/CurrencyBalanceDetails";
import StyledContainer from "../../components/StyledContainer";
import CopyIcon from "../../icons/CopyIcon";
import ReceiveIcon from "../../icons/ReceiveIcon";
import SendIcon from "../../icons/SendIcon";
import StableCoinLogo from "../../icons/StableCoinLogo";
import { Routes } from "../../routes";
import { useWallet } from "../../store/wallet";

export default function Wallet() {
  const navigate = useNavigate();
  const { balance: satsBalance, satsUsdPrice, assets } = useWallet();
  // satsBalance.value = 202020;
  console.log(satsBalance);

  const satsFiatBalance = (satsBalance.value * satsUsdPrice.value).toFixed(2);
  return (
    <div>
      <StyledContainer className="flex h-[180px] w-full flex-col items-center justify-center p-6">
        <div className="flex h-[40px] w-full flex-row items-center justify-end">
          <div className="flex max-w-[80px] flex-row items-center justify-center text-[13px] text-[#F9F9F999]">
            <div className="mr-1 cursor-pointer overflow-hidden text-ellipsis whitespace-nowrap">
              asdfasdasdasdfasdfafdfasdf
            </div>
          </div>
          <div
            className="flex h-4 w-4 cursor-pointer items-center justify-center"
            onClick={() => {
              navigator.clipboard.writeText("asdfasdasdasdfasdfafdfasdf");
            }}
          >
            <CopyIcon stroke="#F9F9F999" />
          </div>
        </div>
        <div className="flex h-[140px] w-full flex-col items-start justify-end gap-2">
          <div className="text-[24px] font-bold">${satsFiatBalance}</div>
          <div className="text-[13px] text-[#F9F9F999]">
            {satsBalance.value} SATs
          </div>
        </div>
      </StyledContainer>
      <div className="mt-6 flex items-center justify-center gap-4">
        <Button
          text="Send"
          icon={<SendIcon strokeWidth="1.5" />}
          kind="primary"
          direction="vertical"
          onClick={() => {
            navigate(Routes.Send);
          }}
          height={84}
        />
        <Button
          text="Receive"
          icon={<ReceiveIcon strokeWidth="1.5" />}
          kind="primary"
          direction="vertical"
          onClick={() => {
            navigate(Routes.Receive);
          }}
          height={84}
        />
      </div>
      <div className="mt-12 w-full border-y border-[#f9f9f9] border-opacity-5">
        <CurrencyBalanceDetails
          logo={<StableCoinLogo strokeWidth="1.50" />}
          currency="Stablecoins"
          fiatBalance="$0.00"
          onClick={() => {
            navigate(Routes.Tokens);
          }}
        />
      </div>
      <div className="mb-8 mt-12 flex flex-col items-center justify-center text-[15px]">
        <span>Your wallet activity starts now</span>
        <div className="mt-2 flex flex-col items-center justify-center text-[13px] text-[#F9F9F999]">
          <span>Add or receive BTC or stablecoins</span>
          <span>to your wallet to get started</span>
        </div>
      </div>
    </div>
  );
}
