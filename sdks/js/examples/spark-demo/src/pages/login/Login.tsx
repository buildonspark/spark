import { generateMnemonic } from "@scure/bip39";
import { wordlist } from "@scure/bip39/wordlists/english";
import { useCallback, useEffect, useMemo, useState } from "react";
import { isValidPhoneNumber } from "react-phone-number-input";
import { useNavigate, useSearchParams } from "react-router-dom";
import { toast } from "react-toastify";
import Button from "../../components/Button";
import CardForm from "../../components/CardForm";
import InitWalletMnemonic from "../../components/InitWalletMnemonic";
import InitWalletMnemonicCheck from "../../components/InitWalletMnemonicCheck";
import PhoneInput from "../../components/PhoneInput";
import RecoverWallet from "../../components/RecoverWallet";
import VerificationCode from "../../components/VerificationCode";
import ChevronIcon from "../../icons/ChevronIcon";
import SparkWithTextLogo from "../../icons/SparkWithTextLogo";
import { Routes } from "../../routes";
import { useWallet } from "../../store/wallet";

enum LoginStep {
  Landing,
  VerificationCode,
  InitWalletMnemonic,
  InitWalletMnemonicCheckOne,
  InitWalletMnemonicCheckTwo,
  RecoverWallet,
}

export default function Login() {
  const [currentStep, setCurrentStep] = useState<LoginStep>(LoginStep.Landing);
  const [searchParams] = useSearchParams();
  const [phoneNumber, setPhoneNumber] = useState<string | undefined>("");
  const [verificationCode, setVerificationCode] = useState<string | undefined>(
    "",
  );
  const [mnemonic, setMnemonic] = useState<string | undefined>(undefined);
  const isValidPhone = useMemo(() => {
    return phoneNumber && isValidPhoneNumber(phoneNumber);
  }, [phoneNumber]);
  const [mnemonicCheckIdx, setMnemonicCheckIdx] = useState<number>(0);
  const [mnemonicCheckInputWord, setMnemonicCheckInputWord] = useState<
    string | undefined
  >(undefined);
  const [mnemonicCheckSuccess, setMnemonicCheckSuccess] = useState<
    boolean | undefined
  >(undefined);

  const isValidVerificationCode = useMemo(() => {
    return verificationCode && verificationCode.length === 6;
  }, [verificationCode]);

  const handleChangeVerificationCode = (value: string) => {
    const numbersOnly = value.replace(/[^0-9]/g, "");
    setVerificationCode(numbersOnly);
  };

  const {
    initWalletFromSeed,
    initWallet,
    setInitWalletNetwork,
    initWalletNetwork,
  } = useWallet();

  const navigate = useNavigate();

  // default the app to REGTEST on load.
  useEffect(() => {
    localStorage.setItem("spark_wallet_network", "REGTEST");
    setInitWalletNetwork("REGTEST");
  }, [setInitWalletNetwork]);

  const handleSubmit = async () => {
    if (currentStep === LoginStep.Landing) {
      await fetch("https://api.dev.dev.sparkinfra.net/graphql/spark/rc", {
        method: "POST",
        headers: {
          "Content-Type": "application/json",
        },
        body: JSON.stringify({
          query: `
            mutation StartSeedRelease($phone: String!) {
              start_seed_release(input: {phone_number: $phone})
            }
          `,
          variables: {
            phone: phoneNumber,
          },
        }),
      });
      setCurrentStep(LoginStep.VerificationCode);
    } else {
      const response = await fetch(
        "https://api.dev.dev.sparkinfra.net/graphql/spark/rc",
        {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
          },
          body: JSON.stringify({
            query: `
              mutation CompleteSeedRelease($phone: String!, $code: String!) { 
                complete_seed_release(input: {phone_number: $phone, code: $code}) {
                  seed
                }
              }
            `,
            variables: {
              phone: phoneNumber,
              code: verificationCode,
            },
          }),
        },
      );

      const data = await response.json();
      const seed = data.data.complete_seed_release.seed;

      await initWalletFromSeed(seed);
      navigate(Routes.Wallet);
    }
  };

  const resetMnemonicState = () => {
    setMnemonic(undefined);
    setMnemonicCheckIdx(0);
    setMnemonicCheckInputWord(undefined);
    setMnemonicCheckSuccess(undefined);
  };

  const logoLeft = useMemo(() => {
    switch (currentStep) {
      case LoginStep.Landing:
        return null;
      default:
        return (
          <ChevronIcon
            direction="left"
            opacity={1}
            height={24}
            width={24}
            strokeWidth={2}
            stroke="rgba(250, 250, 250, 0.80)"
          />
        );
    }
  }, [currentStep]);

  const onLogoLeftClick = useCallback(() => {
    switch (currentStep) {
      case LoginStep.Landing:
        setCurrentStep(LoginStep.VerificationCode);
        break;
      case LoginStep.VerificationCode:
        setCurrentStep(LoginStep.Landing);
        break;
      case LoginStep.InitWalletMnemonic:
        resetMnemonicState();
        setCurrentStep(LoginStep.Landing);
        break;
      case LoginStep.InitWalletMnemonicCheckOne:
        setCurrentStep(LoginStep.InitWalletMnemonic);
        break;
      case LoginStep.InitWalletMnemonicCheckTwo:
        setCurrentStep(LoginStep.InitWalletMnemonicCheckOne);
        break;
      case LoginStep.RecoverWallet:
        resetMnemonicState();
        setCurrentStep(LoginStep.Landing);
        break;
      default:
        return navigate(Routes.Base);
    }
  }, [currentStep, navigate, setCurrentStep]);

  const secondaryButtonText = useMemo(() => {
    switch (currentStep) {
      case LoginStep.InitWalletMnemonic:
        return "Copy Seed Phrase";
      default:
        return "Cancel";
    }
  }, [currentStep]);

  const primaryButtonText = useMemo(() => {
    switch (currentStep) {
      default:
        return "Continue";
    }
  }, [currentStep]);

  const onPrimaryButtonClick = useCallback(async () => {
    const copiedToClipboard = () =>
      toast("Recovery phrase copied to clipboard!");
    const notifySuccess = () => toast("Success!");
    switch (currentStep) {
      case LoginStep.InitWalletMnemonic:
        setMnemonicCheckIdx(Math.floor(Math.random() * 12));
        navigator.clipboard.writeText(mnemonic ?? "");
        copiedToClipboard();
        setCurrentStep(LoginStep.InitWalletMnemonicCheckOne);
        break;
      case LoginStep.InitWalletMnemonicCheckOne:
      case LoginStep.InitWalletMnemonicCheckTwo:
        const trimmedLowerCheckInputWord = mnemonicCheckInputWord
          ?.toLowerCase()
          .trim();
        const trimmedLowerMnemonicWord = mnemonic
          ?.split(" ")
          [mnemonicCheckIdx]?.toLowerCase()
          .trim();
        if (trimmedLowerCheckInputWord === trimmedLowerMnemonicWord) {
          notifySuccess();
          setMnemonicCheckSuccess(undefined);
          setMnemonicCheckInputWord(undefined);
          setMnemonicCheckIdx(Math.floor(Math.random() * 12));
          setCurrentStep(LoginStep.InitWalletMnemonicCheckTwo);
          if (currentStep === LoginStep.InitWalletMnemonicCheckTwo) {
            await initWallet(mnemonic ?? "");
            navigate(Routes.Wallet);
          }
        } else {
          setMnemonicCheckSuccess(false);
        }
        break;
      case LoginStep.RecoverWallet:
        await initWallet(mnemonic ?? "");
        notifySuccess();
        navigate(Routes.Wallet);
        break;
      default:
        return;
    }
  }, [
    currentStep,
    mnemonic,
    setCurrentStep,
    mnemonicCheckIdx,
    mnemonicCheckInputWord,
    initWallet,
    navigate,
  ]);

  const onSecondaryButtonClick = useCallback(() => {
    switch (currentStep) {
      case LoginStep.InitWalletMnemonic:
        const notify = () => toast("Recovery phrase copied to clipboard!");
        navigator.clipboard.writeText(mnemonic ?? "");
        notify();
        break;
      default:
        return navigate(Routes.Base);
    }
  }, [currentStep, mnemonic, navigate]);

  return (
    <div className="flex flex-col items-center justify-center">
      {currentStep === LoginStep.Landing && (
        <>
          {searchParams.get("dev") === "true" && (
            <div className="mb-1 flex w-full flex-row items-center justify-end">
              <div
                className={`font-decimal mr-2 text-center text-[13px] text-[#ffffff] opacity-50`}
              >
                {initWalletNetwork === "MAINNET" ? "Mainnet" : "Regtest"}
              </div>
              <div>
                <button
                  onClick={() => {
                    const newNetwork =
                      initWalletNetwork === "MAINNET" ? "REGTEST" : "MAINNET";
                    localStorage.setItem(
                      "spark_wallet_network",
                      newNetwork.toString(),
                    );
                    setInitWalletNetwork(newNetwork);
                  }}
                  className={`relative inline-flex h-6 w-11 items-center rounded-full transition-colors ${
                    initWalletNetwork === "MAINNET"
                      ? "bg-[#FAFAFACD]"
                      : "bg-[#696969]"
                  }`}
                >
                  <span
                    className={`inline-block h-4 w-4 transform rounded-full transition-transform ${
                      initWalletNetwork === "MAINNET"
                        ? "translate-x-6 bg-[#0A0A0ACD]"
                        : "translate-x-1 bg-[#FAFAFA70]"
                    }`}
                  />
                </button>
              </div>
            </div>
          )}
          <div className="mt-md flex justify-center">
            <SparkWithTextLogo />
          </div>
          <div className="mt-xl text-center font-inter text-[13px] text-[#ffffff] opacity-40">
            A Spark-enabled, self-custody
            <br />
            Bitcoin wallet
          </div>
          <div className="mt-xl">
            <PhoneInput value={phoneNumber} onChange={setPhoneNumber} />
          </div>
          <div className="mt-lg w-full">
            <Button
              text="Continue"
              kind="primary"
              height={44}
              onClick={handleSubmit}
              disabled={currentStep === LoginStep.Landing && !isValidPhone}
            />
          </div>
        </>
      )}
      {currentStep !== LoginStep.Landing && (
        <CardForm
          topTitle={""}
          logoLeft={logoLeft}
          logoLeftClick={onLogoLeftClick}
          primaryButtonClick={onPrimaryButtonClick}
          primaryButtonDisabled={currentStep === LoginStep.VerificationCode}
          primaryButtonText={primaryButtonText}
          secondaryButtonClick={onSecondaryButtonClick}
          secondaryButtonDisabled={currentStep !== LoginStep.InitWalletMnemonic}
          secondaryButtonText={secondaryButtonText}
        >
          {currentStep === LoginStep.VerificationCode && (
            <>
              <div className="w-full">
                <h2 className="text-[20px] font-[600] leading-[25px]">
                  We sent you an SMS code
                </h2>
                <p className="mt-sm flex w-full flex-col text-[15px] font-[500] leading-[20px]">
                  <span className="w-full text-white-50">
                    Enter the 6-digit code we sent to
                  </span>
                  <span className="w-full text-white">{phoneNumber}</span>
                </p>
              </div>
              <div className="mt-xl">
                <VerificationCode
                  value={verificationCode}
                  onChange={handleChangeVerificationCode}
                  onSubmit={handleSubmit}
                />
              </div>
              <div className="mt-lg w-full">
                <Button
                  text="Continue"
                  kind="primary"
                  height={44}
                  onClick={handleSubmit}
                  disabled={
                    currentStep === LoginStep.VerificationCode &&
                    !isValidVerificationCode
                  }
                />
              </div>
            </>
          )}
          {currentStep === LoginStep.InitWalletMnemonic && (
            <InitWalletMnemonic mnemonic={mnemonic} />
          )}
          {currentStep === LoginStep.InitWalletMnemonicCheckOne && (
            <InitWalletMnemonicCheck
              mnemonicCheckIdx={mnemonicCheckIdx}
              setMnemonicCheckInputWord={setMnemonicCheckInputWord}
              mnemonicCheckSuccess={mnemonicCheckSuccess}
            />
          )}
          {currentStep === LoginStep.InitWalletMnemonicCheckTwo && (
            <InitWalletMnemonicCheck
              mnemonicCheckIdx={mnemonicCheckIdx}
              setMnemonicCheckInputWord={setMnemonicCheckInputWord}
              mnemonicCheckSuccess={mnemonicCheckSuccess}
            />
          )}
          {currentStep === LoginStep.RecoverWallet && (
            <RecoverWallet mnemonic={mnemonic} setMnemonic={setMnemonic} />
          )}
        </CardForm>
      )}

      {currentStep === LoginStep.Landing && (
        <>
          <div className="my-lg flex h-[18px] w-full items-center">
            <div className="flex-grow border-t border-white-24 opacity-70"></div>
            <span className="mx-md text-sm text-gray-400">or</span>
            <div className="flex-grow border-t border-white-24 opacity-70"></div>
          </div>
          <div className="flex w-full flex-col gap-md">
            <Button
              text="Add existing wallet"
              kind="secondary"
              height={44}
              onClick={() => {
                setCurrentStep(LoginStep.RecoverWallet);
              }}
            />
            <Button
              text="Setup manually"
              kind="secondary"
              height={44}
              onClick={() => {
                setMnemonic(generateMnemonic(wordlist));
                setCurrentStep(LoginStep.InitWalletMnemonic);
              }}
            />
          </div>
        </>
      )}
    </div>
  );
}
