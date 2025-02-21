import { useNavigate } from "react-router-dom";
import Button from "../../components/Button";
import KeyIcon from "../../icons/KeyIcon";
import SparkleIcon from "../../icons/SparkleIcon";
import WalletIcon from "../../icons/WalletIcon";
import { Routes } from "../../routes";

export default function Login() {
  const navigate = useNavigate();

  return (
    <div className="mx-8">
      <div className="flex flex-col items-center justify-center">
        <div className="flex items-center justify-center gap-3">
          <WalletIcon />
          <div className=" font-decimal font-black text-[32px]">Wallet</div>
        </div>

        <div className="font-decimal text-[#ffffff] text-[13px] text-center mt-4 opacity-40">
          A Spark-enabled, self-custody
          <br />
          Bitcoin wallet
        </div>

        <div className="flex flex-col gap-3 mt-16 w-full">
          <Button
            text="Create a new wallet"
            icon={<SparkleIcon />}
            kind="primary"
            onClick={() => {
              navigate(Routes.WalletSuccess);
            }}
          />
          <Button
            text="I already have a wallet"
            icon={<KeyIcon />}
            kind="secondary"
            onClick={() => {
              navigate(Routes.RecoverWallet);
            }}
          />
        </div>
      </div>
    </div>
  );
}
