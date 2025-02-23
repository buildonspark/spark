import BitcoinIcon from "../icons/BitcoinIcon";
import ChevronRightIcon from "../icons/ChevronRightIcon";
import LightningIcon from "../icons/LightningIcon";
import SparkIcon from "../icons/SparkIcon";
import StyledContainer from "./StyledContainer";

export enum Network {
  NONE = "none",
  LIGHTNING = "lightning",
  BITCOIN = "bitcoin",
  SPARK = "spark",
}

interface NetworksProps {
  onSelectNetwork: (network: Network) => void;
}

export default function Networks({ onSelectNetwork }: NetworksProps) {
  return (
    <div className="mt-4 flex w-full flex-col items-center">
      <StyledContainer
        className="px-4 py-6"
        isPressable
        onClick={() => onSelectNetwork(Network.LIGHTNING)}
      >
        <div className="flex items-center gap-3">
          <LightningIcon />
          <div className="flex flex-grow flex-col gap-[4px] font-decimal">
            <div className="text-left text-[13px]">Lightning invoice</div>
            <div className="flex items-center gap-[1px] text-[12px] opacity-80">
              <div className="rounded-l-[5px] bg-[#232E3D] px-[6px]">
                Instant
              </div>
              <div className="rounded-r-[5px] bg-[#232E3D] px-[6px]">
                Low fees
              </div>
            </div>
          </div>
          <ChevronRightIcon />
        </div>
      </StyledContainer>
      <StyledContainer
        className="px-4 py-6 mt-3"
        isPressable
        onClick={() => onSelectNetwork(Network.SPARK)}
      >
        <div className="flex items-center gap-3">
          <SparkIcon />
          <div className="flex flex-grow flex-col gap-[4px] font-decimal">
            <div className="text-left text-[13px]">Spark</div>
            <div className="flex items-center gap-[1px] text-[12px] opacity-80">
              <div className="rounded-l-[5px] bg-[#232E3D] px-[6px]">
                Instant
              </div>
              <div className="rounded-r-[5px] bg-[#232E3D] px-[6px]">
                No fees
              </div>
            </div>
          </div>
          <ChevronRightIcon />
        </div>
      </StyledContainer>
      <StyledContainer
        className="px-4 py-6 mt-3"
        isPressable
        onClick={() => onSelectNetwork(Network.BITCOIN)}
      >
        <div className="flex items-center gap-3">
          <BitcoinIcon />
          <div className="flex flex-grow flex-col gap-[4px] font-decimal">
            <div className="text-left text-[13px]">Bitcoin</div>
            <div className="flex items-center gap-[1px] text-[12px] opacity-80">
              <div className="rounded-l-[5px] bg-[#232E3D] px-[6px]">
                ~30 min
              </div>
              <div className="rounded-r-[5px] bg-[#232E3D] px-[6px]">
                High fees
              </div>
            </div>
          </div>
          <ChevronRightIcon />
        </div>
      </StyledContainer>
      <div className="fixed bottom-10 mt-20 w-full text-center text-[12px] text-[#f9f9f9] opacity-50">
        Powered by Spark
      </div>
    </div>
  );
}
