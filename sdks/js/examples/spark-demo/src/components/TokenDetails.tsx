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
  const { activeAsset, usdcBalance, mxpBalance } = useWallet();
  return (
    <div className="">
      <StyledContainer className="flex h-[180px] w-full flex-col items-center justify-center">
        <WalletBalance
          asset={activeAsset}
          assetBalance={
            activeAsset.code === "USDC" ? usdcBalance.value : mxpBalance.value
          }
          assetFiatConversion={1 / (activeAsset.usdPrice ?? 1)}
        />
      </StyledContainer>
      <div className="mt-6 flex items-center justify-center gap-4">
        <Button
          text="Send"
          icon={<SendIcon stroke="#000" />}
          kind="primary"
          direction="vertical"
          onClick={onSendButtonClick}
        />
      </div>
    </div>
  );
}
