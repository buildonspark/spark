import SendIcon from "../icons/SendIcon";
import { useWallet } from "../store/wallet";
import Button from "./Button";
import StyledContainer from "./StyledContainer";
import WalletBalance from "./WalletBalance";

export default function TokenDetails({
  onSendButtonClick,
}: {
  onSendButtonClick: () => void;
}) {
  const {
    balance: satsBalance,
    satsUsdPrice,
    assets,
    activeAsset,
  } = useWallet();
  //   satsBalance.value = 202020;
  //   console.log(satsBalance);

  return (
    <div className="">
      <StyledContainer className="flex h-[180px] w-full flex-col items-center justify-center">
        <WalletBalance
          asset={activeAsset}
          assetBalance={Number(activeAsset.balance?.toString() ?? "0")}
          assetFiatConversion={satsUsdPrice.value}
        />
      </StyledContainer>
      <div className="mt-6 flex items-center justify-center gap-4">
        <Button
          text="Send"
          icon={<SendIcon />}
          kind="primary"
          direction="vertical"
          onClick={onSendButtonClick}
        />
      </div>
    </div>
  );
}
