import Button from "../../components/Button";
import StyledContainer from "../../components/StyledContainer";
import ReceiveIcon from "../../icons/ReceiveIcon";
import SendIcon from "../../icons/SendIcon";
import WalletIcon from "../../icons/WalletIcon";

export default function Wallet() {
  return (
    <div className="mx-6">
      <div className="flex items-center justify-center gap-2">
        <WalletIcon className="h-[18px] w-[16px]" />
        <div className="font-decimal font-black text-[24px]">Wallet</div>
      </div>
      <StyledContainer className="mt-9 flex items-center justify-center w-full h-[180px]">
        <div>
          <div className="flex  font-decimal justify-center">
            <div className="text-[24px] self-center">$</div>
            <div className="text-[60px] leading-[60px]">0</div>
            <div className="text-[24px] self-end">.00</div>
          </div>
          <div className=" font-decimal text-[13px] opacity-40">
            0.00000000 BTC
          </div>
        </div>
      </StyledContainer>
      <div className="flex items-center justify-center gap-4 mt-6">
        <Button
          text="Send"
          icon={<SendIcon />}
          kind="primary"
          direction="vertical"
        />
        <Button
          text="Receive"
          icon={<ReceiveIcon />}
          kind="primary"
          direction="vertical"
        />
      </div>
    </div>
  );
}
