import { useMemo, useState } from "react";
import BitcoinIcon from "../icons/BitcoinIcon";
import ChevronIcon from "../icons/ChevronIcon";
import LightningIcon from "../icons/LightningIcon";
import PhoneIcon from "../icons/PhoneIcon";
import SparkIcon from "../icons/SparkIcon";
import DetailsRow from "./DetailsRow";
import { Network } from "./Networks";

/* function debounce<T extends (...args: any[]) => void>(func: T, wait: number) {
  let timeout: ReturnType<typeof setTimeout> | null = null;
  return function (...args: Parameters<T>) {
    if (timeout) clearTimeout(timeout);
    timeout = setTimeout(() => {
      func(...args);
    }, wait);
  };
} */
const formatPhoneNumber = (input: string) => {
  // Remove all non-digit characters
  let digitsOnly = input.replace(/\D/g, "");

  // If the number starts with 1 and has more than 10 digits, it likely includes the country code
  if (digitsOnly.length > 10 && digitsOnly.startsWith("1")) {
    digitsOnly = digitsOnly.substring(1); // Remove the leading 1
  }

  // Format as +1 (000) 000-0000
  if (digitsOnly.length >= 10) {
    return `+1 (${digitsOnly.slice(0, 3)}) ${digitsOnly.slice(3, 6)}-${digitsOnly.slice(6, 10)}`;
  } else if (digitsOnly.length > 6) {
    return `+1 (${digitsOnly.slice(0, 3)}) ${digitsOnly.slice(3, 6)}-${digitsOnly.slice(6)}`;
  } else if (digitsOnly.length > 3) {
    return `+1 (${digitsOnly.slice(0, 3)}) ${digitsOnly.slice(3)}`;
  } else if (digitsOnly.length > 0) {
    return `+1 (${digitsOnly})`;
  }
  return "";
};

const isValidPhoneNumber = (input: string) => {
  const digitsOnly = input.replace(/\D/g, "");
  // Check if it's exactly 10 digits, or 11 digits starting with 1
  return /^[0-9]{10}$/.test(digitsOnly) || /^1[0-9]{10}$/.test(digitsOnly);
};
const capitalizeFirstLetter = (str: string) => {
  return str.charAt(0).toUpperCase() + str.slice(1);
};

// THIS IS NOT A COMPREHENSIVE VALIDATION. DEMO PURPOSE ONLY.
function isValidBitcoinAddress(address: string): boolean {
  // Regex for P2PKH and P2SH addresses
  const legacyRegex = /^[13][a-km-zA-HJ-NP-Z1-9]{25,34}$/;
  // Regex for Bech32 addresses (SegWit)
  const bech32Regex = /^(bc1|bcrt1)[a-z0-9]{25,89}$/;
  // Check against both regex patterns
  return legacyRegex.test(address) || bech32Regex.test(address);
}

// THIS IS NOT A COMPREHENSIVE VALIDATION. DEMO PURPOSE ONLY.
const validateAddress = (address: string): Network => {
  if (/^ln(bc|tb|bcrt)[0-9]{1,}[a-z0-9]+$/.test(address))
    return Network.LIGHTNING;
  if (isValidBitcoinAddress(address)) return Network.BITCOIN;
  if (/^(02|03)[a-fA-F0-9]{64}$/.test(address)) return Network.SPARK;
  if (isValidPhoneNumber(address)) return Network.PHONE;
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

  const logoLeft = useMemo(() => {
    if (inputAddressNetwork === Network.LIGHTNING) return <LightningIcon />;
    if (inputAddressNetwork === Network.BITCOIN) return <BitcoinIcon />;
    if (inputAddressNetwork === Network.SPARK) return <SparkIcon />;
    return null;
  }, [inputAddressNetwork]);

  return (
    <div className="flex w-full flex-col gap-2">
      <input
        className="h-12 w-full rounded-lg border border-solid border-[rgba(249,249,249,0.12)] bg-[#121E2D] px-4 text-[12px]"
        placeholder="Wallet address, Lightning invoice"
        type="text"
        value={inputAddress}
        onChange={handleInputChange}
      />
      {inputAddressNetwork === Network.NONE ? (
        <span className="ml-2 text-[12px] text-[#999999]">
          Works with spark and bitcoin wallet addresses.
        </span>
      ) : (
        inputAddressNetwork !== Network.PHONE && (
          <DetailsRow
            logoLeft={logoLeft}
            title={inputAddress}
            subtitle={`${capitalizeFirstLetter(inputAddressNetwork)} ${inputAddressNetwork === Network.LIGHTNING ? "invoice" : "address"}`}
            logoRight={<ChevronIcon direction="right" />}
            logoLeftCircleBackground={true}
            onClick={() => {
              onAddressSelect(inputAddress, inputAddressNetwork);
            }}
            logoRightMargin={0}
          />
        )
      )}
      {inputAddressNetwork === Network.PHONE && (
        <DetailsRow
          logoLeft={<PhoneIcon />}
          title={`${formatPhoneNumber(inputAddress)}`}
          subtitle={`Send money via text`}
          logoRight={<ChevronIcon />}
          logoLeftCircleBackground={true}
          onClick={() => {
            const digitsOnly = inputAddress.replace(/\D/g, "");
            const parsedAddress =
              digitsOnly.length === 10 ? `+1${digitsOnly}` : `+${digitsOnly}`;
            onAddressSelect(parsedAddress, inputAddressNetwork);
          }}
          logoRightMargin={0}
        />
      )}
    </div>
  );
}
