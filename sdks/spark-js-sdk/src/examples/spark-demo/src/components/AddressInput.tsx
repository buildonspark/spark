import { useState } from "react";
import BitcoinIcon from "../icons/BitcoinIcon";
import ChevronRightIcon from "../icons/ChevronRightIcon";
import LightningIcon from "../icons/LightningIcon";
import SparkIcon from "../icons/SparkIcon";
import DetailsRow from "./DetailsRow";
import { Network } from "./Networks";

function debounce<T extends (...args: any[]) => void>(func: T, wait: number) {
  let timeout: ReturnType<typeof setTimeout> | null = null;
  return function (...args: Parameters<T>) {
    if (timeout) clearTimeout(timeout);
    timeout = setTimeout(() => {
      func(...args);
    }, wait);
  };
}

const capitalizeFirstLetter = (str: string) => {
  return str.charAt(0).toUpperCase() + str.slice(1);
};

// THIS IS NOT A COMPREHENSIVE VALIDATION. DEMO PURPOSE ONLY.
function isValidBitcoinAddress(address: string): boolean {
  // Regex for P2PKH and P2SH addresses
  const legacyRegex = /^[13][a-km-zA-HJ-NP-Z1-9]{25,34}$/;
  // Regex for Bech32 addresses (SegWit)
  const bech32Regex = /^(bc1)[a-z0-9]{25,39}$/;
  // Check against both regex patterns
  return legacyRegex.test(address) || bech32Regex.test(address);
}

// THIS IS NOT A COMPREHENSIVE VALIDATION. DEMO PURPOSE ONLY.
const validateAddress = (address: string): Network => {
  if (/^ln(bc|tb|bcrt)[0-9]{1,}[a-z0-9]+$/.test(address))
    return Network.LIGHTNING;
  if (isValidBitcoinAddress(address)) return Network.BITCOIN;
  if (/^(02|03)[a-fA-F0-9]{64}$/.test(address)) return Network.SPARK;
  return Network.NONE;
};

interface AddressInputProps {
  onAddressSelect: (address: string, addressNetwork: Network) => void;
}

export default function AddressInput({ onAddressSelect }: AddressInputProps) {
  const [inputAddress, setInputAddress] = useState<string>("");
  const [inputAddressNetwork, setInputAddressNetwork] = useState<Network>(
    Network.NONE,
  );

  const handleInputChange = (e: React.ChangeEvent<HTMLInputElement>) => {
    const inputValue = e.target.value;
    setInputAddress(inputValue);
    setInputAddressNetwork(validateAddress(inputValue));
  };

  return (
    <div>
      <input
        className="h-14 w-full rounded-lg border border-solid border-[rgba(249,249,249,0.12)] bg-[#121E2D] px-6"
        placeholder="Wallet address, Lightning invoice"
        type="text"
        value={inputAddress}
        onChange={handleInputChange}
      />
      {inputAddressNetwork === Network.NONE && (
        <span className="ml-2 text-[12px] text-[#999999]">
          Works with spark and bitcoin wallet addresses.
        </span>
      )}
      {inputAddressNetwork === Network.LIGHTNING && (
        // If there are multiple potential addresses, we should be able to set the correct one in the parent with onAddressSelect(inputAddress)
        <DetailsRow
          logoLeft={<LightningIcon />}
          title={inputAddress}
          subtitle={`${capitalizeFirstLetter(inputAddressNetwork)} address`}
          logoRight={<ChevronRightIcon />}
          logoLeftCircleBackground={true}
          onClick={() => {
            onAddressSelect(inputAddress, inputAddressNetwork);
          }}
        />
      )}
      {inputAddressNetwork === Network.BITCOIN && (
        <DetailsRow
          logoLeft={<BitcoinIcon strokeWidth="1.5" />}
          title={inputAddress}
          subtitle={`${capitalizeFirstLetter(inputAddressNetwork)} address`}
          logoRight={<ChevronRightIcon />}
          logoLeftCircleBackground={true}
          onClick={() => {
            onAddressSelect(inputAddress, inputAddressNetwork);
          }}
        />
      )}
      {inputAddressNetwork === Network.SPARK && (
        <DetailsRow
          logoLeft={<SparkIcon />}
          title={inputAddress}
          subtitle={`${capitalizeFirstLetter(inputAddressNetwork)} address`}
          logoRight={<ChevronRightIcon />}
          logoLeftCircleBackground={true}
          onClick={() => {
            onAddressSelect(inputAddress, inputAddressNetwork);
          }}
        />
      )}
    </div>
  );
}
