import { useEffect, useState } from "react";
import { toast } from "react-toastify";
import DetailsRow from "../../components/DetailsRow";
import CopyIcon from "../../icons/CopyIcon";
import { useWallet } from "../../store/wallet";

export default function BitcoinDepositAddress() {
  const [depositAddress, setDepositAddress] = useState<string | null>(null);
  const { getBitcoinDepositAddress } = useWallet();
  const notify = () => toast("Copied!");

  useEffect(() => {
    getBitcoinDepositAddress().then((address) => {
      setDepositAddress(address);
    });
  }, [getBitcoinDepositAddress]);

  return (
    <div className="flex flex-col gap-4">
      <div className="rounded-lg border-[1px] border-[#2d3845]">
        <DetailsRow
          title="Bitcoin Deposit Address"
          subtitle={depositAddress ? depositAddress : "Loading..."}
          logoRight={<CopyIcon />}
          onClick={() => {
            navigator.clipboard.writeText(depositAddress || "");
            notify();
          }}
        />
      </div>
      <div className="text-sm text-gray-500">
        Share this address with the sender to receive funds.
      </div>
    </div>
  );
}
