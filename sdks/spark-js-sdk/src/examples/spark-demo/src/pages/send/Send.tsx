import { useCallback, useState } from "react";
import Button from "../../components/Button";
import DetailsRow from "../../components/DetailsRow";
import BitcoinIcon from "../../icons/BitcoinIcon";
import ChevronRightIcon from "../../icons/ChevronRightIcon";
import { useNavigate } from "react-router-dom";

type AddressType = "Bitcoin" | "Spark" | "Invalid";

function debounce<T extends (...args: any[]) => void>(func: T, wait: number) {
  let timeout: ReturnType<typeof setTimeout> | null = null;
  return function (...args: Parameters<T>) {
    if (timeout) clearTimeout(timeout);
    timeout = setTimeout(() => {
      func(...args);
    }, wait);
  };
}

export default function Send() {
  const [address, setAddress] = useState<string>("");
  const [addressType, setAddressType] = useState<AddressType>("Invalid");
  const navigate = useNavigate();

  const validateAddress = (address: string): AddressType => {
    if (address.length >= 5) return "Bitcoin";
    return "Invalid";
  };
  const handleInputChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const inputValue = e.target.value;
    setAddress(inputValue);
    setAddressType(validateAddress(inputValue));
  };

  return (
    <div>
      <input
        className="w-full h-14 px-6 rounded-lg bg-[#121E2D] border border-solid border-[rgba(249,249,249,0.12)]"
        placeholder="Wallet address, Lightning invoice"
        type="text"
        value={address}
        onChange={handleInputChange}
      />
      {addressType === "Invalid" ? (
        <span className="text-[12px] text-[#999999]">
          Works with spark and bitcoin wallet addresses.
        </span>
      ) : (
        <DetailsRow
          logoLeft={<BitcoinIcon strokeWidth="1.5" />}
          title={address}
          subtitle={`${addressType} address`}
          logoRight={<ChevronRightIcon />}
          logoLeftCircleBackground={true}
          onClick={() => {
            navigate("/amount-input");
          }}
        />
      )}
    </div>
  );
}

const validateAddress = (address: string) => {
  return true;
};
