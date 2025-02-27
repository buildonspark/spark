import { Network } from "@buildonspark/spark-sdk/utils";
import { useEffect, useMemo, useState } from "react";
import { isValidPhoneNumber } from "react-phone-number-input";
import { useNavigate } from "react-router-dom";
import Button from "../../components/Button";
import PhoneInput from "../../components/PhoneInput";
import VerificationCode from "../../components/VerificationCode";
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

  // default the app to regtest on login.
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
      <div className="mb-1 flex w-full flex-row items-center justify-end">
        <div
          className={`mr-2 text-center font-decimal text-[13px] ${
            initWalletNetwork === Network.MAINNET
              ? "text-[#ffffff]"
              : "text-[#ffffff]"
          }`}
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
                ? "bg-white"
                : "bg-[#696969]"
            }`}
          >
            <span
              className={`inline-block h-4 w-4 transform rounded-full transition-transform ${
                initWalletNetwork === Network.MAINNET
                  ? "translate-x-6 bg-black"
                  : "translate-x-1 bg-white"
              }`}
            />
          </button>
        </div>
      </div>
      <div className="font-inter mt-4 text-center text-[13px] text-[#ffffff] opacity-40">
        A Spark-enabled, self-custody
        <br />
        Bitcoin wallet
      </div>
      <div className="mt-16">
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
      <div className="mt-32 w-full">
        <Button
          text="Submit"
          kind="primary"
          onClick={handleSubmit}
          disabled={
            (loginState === LoginState.PhoneInput && !isValidPhone) ||
            (loginState === LoginState.VerificationCode &&
              !isValidVerificationCode)
          }
        />
      </div>
    </div>
  );
}
