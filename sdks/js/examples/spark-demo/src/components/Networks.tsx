import BitcoinIcon from "../icons/BitcoinIcon";
import ChevronIcon from "../icons/ChevronIcon";
import LightningIcon from "../icons/LightningIcon";
import SparkIcon from "../icons/SparkIcon";
import StyledContainer from "./StyledContainer";

export enum Network {
  NONE = "none",
  LIGHTNING = "lightning",
  BITCOIN = "bitcoin",
  SPARK = "spark",
  PHONE = "phone",
}

interface NetworksProps {
  onSelectNetwork: (network: Network) => void;
}

export default function Networks({ onSelectNetwork }: NetworksProps) {
  return (
    <div className="flex w-full flex-col items-center">
      <StyledContainer
        className="mt-xs px-4 py-6"
        isPressable
        onClick={() => onSelectNetwork(Network.SPARK)}
      >
        <div className="flex items-center gap-3">
          <SparkIcon />
          <div className="flex flex-grow flex-col gap-[4px] font-inter">
            <div className="text-left text-[13px]">Spark address</div>
            <div className="flex items-center gap-[1px] text-[12px] text-gray-800">
              <div className="rounded-l-[5px] bg-white-6 px-[6px]">Instant</div>
              <div className="rounded-r-[5px] bg-white-6 px-[6px] text-blue">
                Free
              </div>
            </div>
          </div>
          <ChevronIcon direction="right" />
        </div>
      </StyledContainer>
      <StyledContainer
        className="mt-xs px-4 py-6"
        isPressable
        onClick={() => onSelectNetwork(Network.LIGHTNING)}
      >
        <div className="flex items-center gap-3">
          <LightningIcon />
          <div className="flex flex-grow flex-col gap-[4px] font-inter">
            <div className="text-left text-[13px]">Lightning invoice</div>
            <div className="flex items-center gap-[1px] text-[12px] text-gray-800">
              <div className="rounded-l-[5px] bg-white-6 px-[6px]">Instant</div>
              <div className="bg-white-6 px-[6px]">Low fees</div>
              <div className="rounded-r-[5px] bg-white-6 px-[6px] text-green">
                Best privacy
              </div>
            </div>
          </div>
          <ChevronIcon direction="right" />
        </div>
      </StyledContainer>
      <StyledContainer
        className="mt-xs px-4 py-6"
        isPressable
        onClick={() => onSelectNetwork(Network.BITCOIN)}
      >
        <div className="flex items-center gap-3">
          <BitcoinIcon />
          <div className="flex flex-grow flex-col gap-[4px] font-inter">
            <div className="text-left text-[13px]">Bitcoin address</div>
            <div className="flex items-center gap-[1px] text-[12px] text-gray-800">
              <div className="rounded-l-[5px] bg-white-6 px-[6px]">L1</div>
              <div className="bg-white-6 px-[6px]">~30 min</div>
              <div className="rounded-r-[5px] bg-white-6 px-[6px] text-red">
                High fees
              </div>
            </div>
          </div>
          <ChevronIcon direction="right" />
        </div>
      </StyledContainer>
    </div>
  );
}
