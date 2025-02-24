import { useNavigate } from "react-router-dom";
import Button from "../../components/Button";
import CurrencyBalanceDetails from "../../components/CurrencyBalanceDetails";
import StyledContainer from "../../components/StyledContainer";
import WalletBalance from "../../components/WalletBalance";
import ReceiveIcon from "../../icons/ReceiveIcon";
import SendIcon from "../../icons/SendIcon";
import StableCoinLogo from "../../icons/StableCoinLogo";
import WalletIcon from "../../icons/WalletIcon";
import { Routes } from "../../routes";
import { useWallet } from "../../store/wallet";

export default function Wallet() {
  const navigate = useNavigate();
  const { balance: satsBalance, satsUsdPrice, assets } = useWallet();
  // satsBalance.value = 202020;
  console.log(satsBalance);

  return (
    <div className="mx-6">
      <div className="flex items-center justify-center gap-2">
        <WalletIcon className="h-[18px] w-[16px]" />
        <div className="font-decimal text-[24px] font-black">Wallet</div>
      </div>
      <StyledContainer className="mt-9 flex h-[180px] w-full items-center justify-center">
        <WalletBalance
          asset={assets.BTC}
          assetBalance={satsBalance.value}
          assetFiatConversion={satsUsdPrice.value}
        />
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
      <div className="mt-6 w-full border-y border-[#f9f9f9] border-opacity-10">
        <CurrencyBalanceDetails
          logo={<StableCoinLogo strokeWidth="1.50" />}
          currency="Stablecoins"
          fiatBalance="$0.00"
          onClick={() => {
            navigate(Routes.Tokens);
          }}
        />
      </div>
    </div>
  );
}
