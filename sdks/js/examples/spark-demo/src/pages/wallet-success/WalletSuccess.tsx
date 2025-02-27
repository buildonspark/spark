import { useState } from "react";
import Button from "../../components/Button";
import StyledContainer from "../../components/StyledContainer";
import CopyIcon from "../../icons/CopyIcon";

import { useNavigate } from "react-router-dom";
import { toast } from "react-toastify";
import WalletIcon from "../../icons/WalletIcon";
import { Routes } from "../../routes";
import { useWallet } from "../../store/wallet";

export default function WalletSuccess() {
  const [mnemonic, setMnemonic] = useState<string | null>(null);
  const notify = () => toast("Copied!");
  const navigate = useNavigate();
  const { initWallet } = useWallet();

  // useEffect(() => {
  //   generatorMnemonic().then((mnemonic) => {
  //     setMnemonic(mnemonic);
  //   });
  // }, [generatorMnemonic]);

  const onContinue = async () => {
    if (!mnemonic) return;
    await initWallet(mnemonic);
    navigate(Routes.Wallet);
  };

  if (!mnemonic) return null;

  return (
    <div className="mx-6">
      <div className="flex items-center justify-center gap-2">
        <WalletIcon className="h-[18px] w-[16px]" />
        <div className="font-inter text-[24px] font-black">Wallet</div>
      </div>
      <StyledContainer className="mt-9 flex min-h-[180px] w-full items-center justify-center">
        <div className="flex h-full w-full flex-wrap">
          {mnemonic
            ?.split(" ")
            .map((word) => <div className="w-1/3 p-2 text-center">{word}</div>)}
        </div>
      </StyledContainer>
      <div className="mt-6 flex flex-col items-center justify-center gap-4">
        <Button
          icon={
            <div className="flex h-10 items-center gap-2">
              Copy seed phrase
              <CopyIcon />
            </div>
          }
          kind="secondary"
          onClick={() => {
            navigator.clipboard.writeText(mnemonic ?? "");
            notify();
          }}
        />
        <Button
          icon={<div className="flex h-10 items-center gap-2">Continue</div>}
          kind="primary"
          onClick={onContinue}
        />
      </div>
    </div>
  );
}
