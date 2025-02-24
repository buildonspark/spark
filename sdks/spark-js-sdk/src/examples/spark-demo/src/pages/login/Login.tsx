import { useMemo, useState } from "react";
import { isValidPhoneNumber } from "react-phone-number-input";
import { useNavigate } from "react-router-dom";
import Button from "../../components/Button";
import PhoneInput from "../../components/PhoneInput";
import VerificationCode from "../../components/VerificationCode";
import WalletIcon from "../../icons/WalletIcon";
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

  const wallet = useWallet();

  const navigate = useNavigate();

  const handleSubmit = async () => {
    if (loginState === LoginState.PhoneInput) {
      const response = await fetch(
        "https://api.dev.dev.sparkinfra.net/graphql/spark/rc",
        {
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
              phone: "+18163921294",
            },
          }),
        },
      );
      setLoginState(LoginState.VerificationCode);
    } else {
      // TODO: Currently not receiving the OTP, confirm here once we have it
      navigate(Routes.Wallet);
    }
  };

  return (
    <div className="mx-8">
      <div className="flex flex-col items-center justify-center">
        <div className="flex items-center justify-center gap-3">
          <WalletIcon />
          <div className="font-decimal text-[32px] font-black">Wallet</div>
        </div>

        <div className="mt-4 text-center font-decimal text-[13px] text-[#ffffff] opacity-40">
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
    </div>
  );
}
