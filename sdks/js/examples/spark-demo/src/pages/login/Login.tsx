import { Network } from "@buildonspark/spark-sdk/utils";
import { useEffect, useMemo, useState } from "react";
import { isValidPhoneNumber } from "react-phone-number-input";
import { useNavigate, useSearchParams } from "react-router-dom";
import Button from "../../components/Button";
import PhoneInput from "../../components/PhoneInput";
import VerificationCode from "../../components/VerificationCode";
import SparkWithTextLogo from "../../icons/SparkWithTextLogo";
import { Routes } from "../../routes";
import { useWallet } from "../../store/wallet";

enum LoginState {
  PhoneInput,
  VerificationCode,
}

export default function Login() {
  const [loginState, setLoginState] = useState<LoginState>(
    LoginState.PhoneInput,
  );
  const [searchParams] = useSearchParams();
  const [phoneNumber, setPhoneNumber] = useState<string | undefined>("");
  const [verificationCode, setVerificationCode] = useState<string | undefined>(
    "",
  );

  const isValidPhone = useMemo(() => {
    return phoneNumber && isValidPhoneNumber(phoneNumber);
  }, [phoneNumber]);

  const isValidVerificationCode = useMemo(() => {
    return verificationCode && verificationCode.length === 6;
  }, [verificationCode]);

  const handleChangeVerificationCode = (value: string) => {
    const numbersOnly = value.replace(/[^0-9]/g, "");
    setVerificationCode(numbersOnly);
  };

  const { initWalletFromSeed, setInitWalletNetwork, initWalletNetwork } =
    useWallet();

  const navigate = useNavigate();

  // default the app to mainnet on load.
  useEffect(() => {
    localStorage.setItem("spark_wallet_network", Network.MAINNET.toString());
    setInitWalletNetwork(Network.MAINNET);
  }, [setInitWalletNetwork]);

  const handleSubmit = async () => {
    if (loginState === LoginState.PhoneInput) {
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
      setLoginState(LoginState.VerificationCode);
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

  return (
    <div className="flex flex-col items-center justify-center">
      {searchParams.get("dev") === "true" && (
        <div className="mb-1 flex w-full flex-row items-center justify-end">
          <div
            className={`mr-2 text-center font-decimal text-[13px] text-[#ffffff] opacity-50`}
          >
            {initWalletNetwork === Network.MAINNET ? "Mainnet" : "Regtest"}
          </div>
          <div>
            <button
              onClick={() => {
                const newNetwork =
                  initWalletNetwork === Network.MAINNET
                    ? Network.REGTEST
                    : Network.MAINNET;
                localStorage.setItem(
                  "spark_wallet_network",
                  newNetwork.toString(),
                );
                setInitWalletNetwork(newNetwork);
              }}
              className={`relative inline-flex h-6 w-11 items-center rounded-full transition-colors ${
                initWalletNetwork === Network.MAINNET
                  ? "bg-[#FAFAFACD]"
                  : "bg-[#696969]"
              }`}
            >
              <span
                className={`inline-block h-4 w-4 transform rounded-full transition-transform ${
                  initWalletNetwork === Network.MAINNET
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
      <div className="font-inter mt-xl text-center text-[13px] text-[#ffffff] opacity-40">
        A Spark-enabled, self-custody
        <br />
        Bitcoin wallet
      </div>
      <div className="mt-xl">
        {loginState === LoginState.PhoneInput && (
          <PhoneInput value={phoneNumber} onChange={setPhoneNumber} />
        )}
        {loginState === LoginState.VerificationCode && (
          <VerificationCode
            value={verificationCode}
            onChange={handleChangeVerificationCode}
            onSubmit={handleSubmit}
          />
        )}
      </div>
      <div className="mt-lg w-full">
        <Button
          text="Continue"
          kind="primary"
          height={44}
          onClick={handleSubmit}
          disabled={
            (loginState === LoginState.PhoneInput && !isValidPhone) ||
            (loginState === LoginState.VerificationCode &&
              !isValidVerificationCode)
          }
        />
      </div>
      {loginState !== LoginState.VerificationCode && (
        <>
          <div className="my-lg flex h-[18px] w-full items-center">
            <div className="border-white-24 flex-grow border-t opacity-70"></div>
            <span className="mx-md text-sm text-gray-400">or</span>
            <div className="border-white-24 flex-grow border-t opacity-70"></div>
          </div>
          <div className="gap-md flex w-full flex-col">
            <Button
              text="Add existing wallet"
              kind="secondary"
              height={44}
              disabled={true}
              onClick={() => {
                // navigate(Routes.RecoverWallet);
              }}
            />
            <Button
              text="Setup manually"
              kind="secondary"
              height={44}
              disabled={true}
              onClick={() => {
                // navigate(Routes.WalletSuccess);
              }}
            />
          </div>
        </>
      )}
    </div>
  );
}
