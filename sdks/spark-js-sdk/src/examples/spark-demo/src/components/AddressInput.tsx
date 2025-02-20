import { useState } from "react";
import { Network } from "./Networks";
import DetailsRow from "./DetailsRow";
import BitcoinIcon from "../icons/BitcoinIcon";
import ChevronRightIcon from "../icons/ChevronRightIcon";

function debounce<T extends (...args: any[]) => void>(func: T, wait: number) {
  let timeout: ReturnType<typeof setTimeout> | null = null;
  return function (...args: Parameters<T>) {
    if (timeout) clearTimeout(timeout);
    timeout = setTimeout(() => {
      func(...args);
    }, wait);
  };
}

interface AddressInputProps {
  onAddressSelect: (address: string, addressNetwork: Network) => void;
}

export default function AddressInput({ onAddressSelect }: AddressInputProps) {
  const [inputAddress, setInputAddress] = useState<string>("");
  const [inputAddressNetwork, setInputAddressNetwork] = useState<Network>(
    Network.NONE
  );
  const validateAddress = (address: string): Network => {
    if (address.length >= 5) return Network.LIGHTNING;
    return Network.NONE;
  };
  const handleInputChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const inputValue = e.target.value;
    setInputAddress(inputValue);
    setInputAddressNetwork(validateAddress(inputValue));
  };

  return (
    <div>
      <input
        className="w-full h-14 px-6 rounded-lg bg-[#121E2D] border border-solid border-[rgba(249,249,249,0.12)]"
        placeholder="Wallet address, Lightning invoice"
        type="text"
        value={inputAddress}
        onChange={handleInputChange}
      />
      {inputAddressNetwork === Network.NONE ? (
        <span className="text-[12px] text-[#999999]">
          Works with spark and bitcoin wallet addresses.
        </span>
      ) : inputAddressNetwork === Network.LIGHTNING ? (
        // If there are multiple potential addresses, we should be able to set the correct one in the parent with onAddressSelect(inputAddress)
        <DetailsRow
          logoLeft={<BitcoinIcon strokeWidth="1.5" />}
          title={inputAddress}
          subtitle={`${inputAddressNetwork} address`}
          logoRight={<ChevronRightIcon />}
          logoLeftCircleBackground={true}
          onClick={() => {
            onAddressSelect(inputAddress, inputAddressNetwork);
          }}
        />
      ) : null}
    </div>
  );
}
