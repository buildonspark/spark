import { useState } from "react";
import Button from "../../components/Button";
import StyledContainer from "../../components/StyledContainer";
import CopyIcon from "../../icons/CopyIcon";

import WalletIcon from "../../icons/WalletIcon";
import { useNavigate } from "react-router-dom";

export default function WalletSuccess() {
  const [mnemonic, setMnemonic] = useState(
    "CAFEBABE CAFEBABE CAFEBABE CAFEBABE CAFEBABE CAFEBABE CAFEBABE CAFEBABE CAFEBABE CAFEBABE CAFEBABE CAFEBABE"
  );
  const navigate = useNavigate();

  return (
    <div className="mx-6">
      <div className="flex items-center justify-center gap-2">
        <WalletIcon className="h-[18px] w-[16px]" />
        <div className="font-decimal font-black text-[24px]">Wallet</div>
      </div>
      <StyledContainer className="mt-9 flex items-center justify-center w-full min-h-[180px]">
        <div className="w-full h-full flex flex-wrap">
          {mnemonic.split(" ").map((word) => (
            <div className="w-1/3 p-2 text-center">{word}</div>
          ))}
        </div>
      </StyledContainer>
      <div className="flex flex-col items-center justify-center gap-4 mt-6">
        <Button
          icon={
            <div className="flex items-center h-10 gap-2">
              Copy seed phrase
              <CopyIcon />
            </div>
          }
          kind="secondary"
          onClick={() => {
            navigator.clipboard.writeText(mnemonic);
            alert("Copied to clipboard");
          }}
        />
        <Button
          icon={<div className="flex items-center h-10 gap-2">Continue</div>}
          kind="primary"
          onClick={() => navigate("/wallet")}
        />
      </div>
    </div>
  );
}
