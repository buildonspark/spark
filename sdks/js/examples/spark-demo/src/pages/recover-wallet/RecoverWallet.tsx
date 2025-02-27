import clsx from "clsx";
import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import styled from "styled-components";
import Button from "../../components/Button";
import StyledContainer from "../../components/StyledContainer";
import { Routes } from "../../routes";
import { useWallet } from "../../store/wallet";
// TODO: The mnemonic phrase could be any length, but hardcoding as 12 for now
const MNEMONIC_WORDS = 12;
export default function RecoverWallet() {
  const [mnemonic, setMnemonic] = useState<string[]>([]);
  const { initWallet } = useWallet();
  const navigate = useNavigate();
  useEffect(() => {
    const listener = (e: ClipboardEvent) => {
      const newMnemonic = e.clipboardData?.getData("text").split(" ");
      if (newMnemonic && newMnemonic.length === MNEMONIC_WORDS) {
        setMnemonic(newMnemonic);
      }
    };

    window.addEventListener("paste", listener);

    return () => {
      window.removeEventListener("paste", listener);
    };
  }, []);

  const onClickPaste = async () => {
    const text = await window.navigator.clipboard.readText();
    const newMnemonic = text.split(" ");
    if (newMnemonic && newMnemonic.length === MNEMONIC_WORDS) {
      setMnemonic(newMnemonic);
    }
  };

  const onClickRecover = async () => {
    if (mnemonic.length === MNEMONIC_WORDS) {
      await initWallet(mnemonic.join(" "));
      navigate(Routes.Wallet);
    }
  };

  return (
    <div
      className={
        "flex flex-col items-center rounded-3xl border-[0.5px] border-[#f9f9f9] border-opacity-25 p-8 pb-16"
      }
    >
      <div className="font-inter text-[18px]">Recover your wallet</div>
      <div className="text-[13px] opacity-50">Past your Seed Phrase</div>
      <StyledContainer className="mb-6 mt-9 flex min-h-[180px] w-full items-center justify-center px-12 py-9">
        <div className="flex flex-1 flex-col">
          {Array.from({ length: MNEMONIC_WORDS / 2 }).map((_, index) => (
            <div key={index} className="flex items-center p-2 text-center">
              <div className="mr-[6px] w-[24px] text-right">{index + 1}.</div>
              {mnemonic[index] ? <div>{mnemonic[index]}</div> : <EmptyWord />}
            </div>
          ))}
        </div>
        <div className="flex flex-1 flex-col">
          {Array.from({ length: MNEMONIC_WORDS / 2 }).map((_, index) => (
            <div key={index} className="flex items-center p-2 text-center">
              <div className="mr-[6px] w-[24px] text-right">{index + 7}.</div>
              {mnemonic[index + 6] ? (
                <div className="text-center">{mnemonic[index + 6]}</div>
              ) : (
                <EmptyWord />
              )}
            </div>
          ))}
        </div>
        {mnemonic.length === 0 && (
          <div className="absolute left-12 right-12">
            <Button
              text="Paste from clipboard"
              kind="primary"
              opaque
              onClick={onClickPaste}
            />
          </div>
        )}
      </StyledContainer>
      <div className={clsx("w-full", mnemonic.length === 0 && "opacity-50")}>
        <Button
          text="Recover wallet"
          kind="primary"
          disabled={mnemonic.length === 0}
          onClick={onClickRecover}
        />
      </div>
    </div>
  );
}

const EmptyWord = styled.div`
  width: 75px;
  height: 18px;
  background: #f9f9f90a;
  border-radius: 12px;
`;
