import ChevronRightIcon from "../icons/ChevronRightIcon";
import LightningIcon from "../icons/LightningIcon";
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
    <div className="flex w-full flex-col items-center mt-4">
      <StyledContainer
        className="py-6 px-4"
        isPressable
        onClick={() => onSelectNetwork(Network.LIGHTNING)}
      >
        <div className="flex items-center gap-3">
          <LightningIcon />
          <div className="flex flex-col flex-grow font-decimal gap-[4px]">
            <div className="text-[13px] text-left">Lightning invoice</div>
            <div className="flex items-center gap-[1px] text-[12px] opacity-80">
              <div className="bg-[#232E3D] rounded-l-[5px] px-[6px]">
                Instant
              </div>
              <div className="bg-[#232E3D] rounded-r-[5px] px-[6px]">
                Low fees
              </div>
            </div>
          </div>
          <ChevronRightIcon />
        </div>
      </StyledContainer>
      <div className="fixed bottom-10 w-full text-center text-[12px] text-[#f9f9f9] opacity-50 mt-20">
        Powered by Spark
      </div>
    </div>
  );
}
