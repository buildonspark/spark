import {
  Popover,
  PopoverContent,
  PopoverTrigger,
} from "@radix-ui/react-popover";
import clsx from "clsx";
import { useState } from "react";
import ReactPhoneInput, {
  Country,
  FlagProps,
  getCountryCallingCode,
} from "react-phone-number-input";
import flags from "react-phone-number-input/flags";
import styled from "styled-components";
import ChevronIcon from "../icons/ChevronIcon";

interface PhoneInputProps {
  value: string | undefined;
  onChange: (phoneNumber: string | undefined) => void;
}

export default function PhoneInput({ value, onChange }: PhoneInputProps) {
  return (
    <div className="flex flex-col gap-2">
      <ReactPhoneInput
        className="flex flex-row rounded-[8px] bg-transparent"
        onChange={onChange}
        defaultCountry="US"
        id="phone"
        value={value}
        inputComponent={InputComponent}
        countrySelectComponent={CountrySelectComponent}
        flagComponent={FlagComponent}
        countryCallingCodeEditable={false}
        placeholder="Enter phone"
        international={false}
        autoComplete="off"
        addInternationalOption={false}
      />
    </div>
  );
}

type CountrySelectOption = { label: string; value: Country };

type CountrySelectProps = {
  disabled?: boolean;
  value: Country;
  onChange: (value: Country) => void;
  options: CountrySelectOption[];
};

const CountrySelectComponent = ({
  disabled,
  value,
  onChange,
  options,
}: CountrySelectProps) => {
  const [search, setSearch] = useState("");

  const filteredOptions = options
    .filter((option) => option.value)
    .filter(
      (option) =>
        option.label.toLowerCase().includes(search.toLowerCase()) ||
        option.value.toLowerCase().includes(search.toLowerCase()),
    );

  return (
    <Popover>
      <PopoverTrigger>
        <PopoverButton
          className={clsx(
            "mr-2 flex h-10 rounded-lg border border-[#3a3a3a] px-3 py-1 font-bold",
            disabled ? "opacity-50" : "",
          )}
        >
          <FlagComponent country={value} countryName={value} />
          <ChevronIcon direction="down" stroke="#FAFAFA" opacity={1} />
        </PopoverButton>
      </PopoverTrigger>

      <PopoverContent className="bg-popover z-[10] w-[350px] rounded-[8px] border-[1px] border-[#f9f9f934] bg-[#010101]">
        <input
          onChange={(e) => setSearch(e.target.value)}
          value={search}
          className="w-full border-b border-[#f9f9f934] bg-transparent px-4 py-4 outline-none"
          autoFocus={false}
          placeholder="Search countries..."
        />
        <div className="max-h-[500px] overflow-auto">
          {filteredOptions.length === 0 && (
            <div className="px-4 py-4 text-center text-sm text-[#f9f9f980]">
              No results found
            </div>
          )}
          {filteredOptions.map((option) => {
            const Flag = flags[option.value];
            return (
              <button
                key={option.value}
                onClick={() => onChange(option.value)}
                className="flex w-full items-center justify-between px-3 py-4 font-bold hover:bg-[#f9f9f924]"
              >
                <div className="flex items-center gap-2">
                  <div className="w-6">
                    {Flag && <Flag title={option.value} />}
                  </div>
                  <div className="text-start">{option.label}</div>
                  <div className="text-xs text-[#f9f9f980]">
                    +{getCountryCallingCode(option.value)}
                  </div>
                </div>
              </button>
            );
          })}
        </div>
      </PopoverContent>
    </Popover>
  );
};

const FlagComponent = ({ country, countryName }: FlagProps) => {
  const Flag = flags[country];

  return (
    <div className="flex-1 whitespace-nowrap">
      {Flag && (
        <div className="text-15/20 flex h-4 flex-row items-center border-[#3a3a3a] font-normal">
          {/* <div className="w-4">
            <Flag title={countryName} />
          </div> */}
          <div>+{getCountryCallingCode(country)}</div>
        </div>
      )}
    </div>
  );
};

const PopoverButton = styled.div`
  display: flex;
  align-items: center;
  justify-content: center;
`;

const InputComponent = styled.input`
  height: 40px;
  padding-top: 9px;
  padding: 4px 16px;
  display: flex;
  width: 257px;
  border-radius: 8px;
  border: 1px solid #3a3a3a;
  background: #0a0a0a;
  color: #fafafa;
  font-size: 15px;
  font-weight: 400;
  line-height: 20px;
  letter-spacing: -0.187px;
  font-family: "Inter", sans-serif;

  outline: none;
`;
