import { useState } from "react";
import StyledContainer from "./StyledContainer";
import WalletIcon from "../icons/WalletIcon";
import styled from "styled-components";
import ClockIcon from "../icons/ClockIcon";
import CopyIcon from "../icons/CopyIcon";
import PencilIcon from "../icons/PencilIcon";
import Button from "./Button";
type ActiveButton = "bitcoin" | "lightning" | "uma";

const ACTIVE_BUTTON_STYLES =
  "rounded-[6px] border border-[rgba(249,249,249,0.12)] bg-[#0E3154] shadow-[0px_4px_6px_0px_rgba(0,0,0,0.14),0px_0px_0px_1px_#0C0D0F,0px_9px_14px_-5px_rgba(255,255,255,0.10)_inset]";

const INACTIVE_BUTTON_STYLES =
  "rounded-[6px] border border-transparent bg-transparent shadow-none";

export default function ReceiveDetailsPage() {
  const [active, setActive] = useState<ActiveButton>("lightning");
  return (
    <div className="flex flex-col items-center mt-4 w-full">
      <div className="flex py-4 px-6 w-full">
        <div
          className={`flex-1 text-[12px] min-h-5 p-3 text-center rounded-md ${
            active === "bitcoin" ? ACTIVE_BUTTON_STYLES : INACTIVE_BUTTON_STYLES
          }`}
          onClick={() => setActive("bitcoin")}
        >
          Bitcoin
        </div>
        <div
          className={`flex-1 text-[12px] min-h-5 p-3 text-center rounded-md ${
            active === "lightning"
              ? ACTIVE_BUTTON_STYLES
              : INACTIVE_BUTTON_STYLES
          }`}
          onClick={() => setActive("lightning")}
        >
          Lightning
        </div>
        <div
          className={`flex-1 text-[12px] min-h-5 p-3 text-center rounded-md ${
            active === "uma" ? ACTIVE_BUTTON_STYLES : INACTIVE_BUTTON_STYLES
          }`}
          onClick={() => setActive("uma")}
        >
          Uma
        </div>
      </div>
      <ReceiveQRCard>
        <div className="w-full h-full rounded-2xl flex flex-col items-right justify-between">
          <WalletIcon className="m-6 w-4" />
          <div className="text-[12px] text-[#f9f9f9] opacity-50 m-6">
            Powered by Spark
          </div>
        </div>
        <div className="flex items-center w-full h-full rounded-2xl">
          <div
            className="m-3 w-[160px] h-[160px] rounded-xl"
            style={{ backgroundColor: "rgba(33, 43, 55, 0.6)" }}
          ></div>
        </div>
      </ReceiveQRCard>
      <ReceiveDetailsContainer>
        <DetailsRow title="Amount" subtitle="1000" logoRight={<PencilIcon />} />
        <DetailsRow
          borderTop={true}
          title="Lightning Invoice"
          subtitle="ln123...456"
          logoRight={<CopyIcon />}
        />
        <DetailsRow
          logoLeft={<ClockIcon />}
          borderTop={true}
          //   title="Amount"
          subtitle="1000"
          //   logo={<WalletIcon />}
        />
      </ReceiveDetailsContainer>
      <Button text="Share" />
      <div className="text-[12px] text-[#f9f9f9] opacity-50 mt-20">
        Powered by Spark
      </div>
    </div>
  );
}
type DetailsRowProps = {
  borderTop?: boolean;
  title?: string;
  subtitle?: string;
  logoRight?: React.ReactNode;
  logoLeft?: React.ReactNode;
};
const DetailsRow = ({
  title,
  subtitle,
  logoRight,
  logoLeft,
  borderTop = false,
}: DetailsRowProps) => {
  return (
    <div
      className={`h-[72px] flex flex-row items-center justify-between ${
        borderTop ? "border-t border-[#2d3845]" : ""
      }`}
    >
      <div className="flex flex-row items-center">
        {logoLeft && <div className="flex items-center pl-4">{logoLeft}</div>}
        <div
          className={`flex flex-col justify-between ${
            !logoLeft ? "pl-4" : "pl-2"
          }`}
        >
          {title && <div className="text-[12px] text-[#f9f9f9]">{title}</div>}
          {subtitle && (
            <div className="text-[12px] text-[#f9f9f9] opacity-50">
              {subtitle}
            </div>
          )}
        </div>
      </div>
      {logoRight && (
        <div className="flex flex-col items-center justify-between pr-4">
          {logoRight}
        </div>
      )}
    </div>
  );
};

const ReceiveDetailsContainer = styled.div`
  margin-top: 8px;
  width: 345px;
  display: flex;
  flex-direction: column;
  border-radius: 16px;
  border: 0.33px solid #2d3845;
`;
const ReceiveQRCard = styled.div`
  width: 345px;
  height: 184px;
  display: flex;
  border-radius: 24px;
  border: 0.5px solid rgba(249, 249, 249, 0.25);
  margin-bottom: 8px;

  background: linear-gradient(
    180deg,
    #141a22 0%,
    #141a22 11.79%,
    #131a22 21.38%,
    #131922 29.12%,
    #131922 35.34%,
    #131921 40.37%,
    #131921 44.56%,
    #121820 48.24%,
    #121820 51.76%,
    #12171f 55.44%,
    #11171f 59.63%,
    #11171e 64.66%,
    #11161e 70.88%,
    #11161d 78.62%,
    #10151c 88.21%,
    #10151c 100%
  );

  box-shadow: 0px 216px 60px 0px rgba(0, 0, 0, 0),
    0px 138px 55px 0px rgba(0, 0, 0, 0.01),
    0px 78px 47px 0px rgba(0, 0, 0, 0.05), 0px 35px 35px 0px rgba(0, 0, 0, 0.09),
    0px 9px 19px 0px rgba(0, 0, 0, 0.1);
`;
