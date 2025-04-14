import { useEffect, useState } from "react";
import { toast } from "react-toastify";
import DetailsRow from "../../components/DetailsRow";
import CopyIcon from "../../icons/CopyIcon";
import { useWallet } from "../../store/wallet";

export default function SparkDepositAddress() {
  const [masterPublicKey, setMasterPublicKey] = useState<string | null>(null);
  const { getMasterPublicKey } = useWallet();
  const notify = () => toast("Copied!");

  useEffect(() => {
    getMasterPublicKey().then((publicKey) => {
      setMasterPublicKey(publicKey);
    });
  }, [getMasterPublicKey]);

  return (
    <div className="flex flex-col gap-4">
      <div className="rounded-lg border-[1px] border-[#2d3845]">
        <DetailsRow
          title="Spark Deposit Address"
          subtitle={masterPublicKey}
          logoRight={<CopyIcon />}
          onClick={() => {
            navigator.clipboard.writeText(masterPublicKey || "");
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
