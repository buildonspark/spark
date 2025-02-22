import { QRCodeSVG } from "qrcode.react";
import { useEffect, useRef } from "react";
import styled from "styled-components";
import ClockIcon from "../icons/ClockIcon";
import CloseIcon from "../icons/CloseIcon";
import CopyIcon from "../icons/CopyIcon";
import PencilIcon from "../icons/PencilIcon";
import WalletIcon from "../icons/WalletIcon";
import DetailsRow from "./DetailsRow";

export default function ReceiveDetails({
  qrCodeModalVisible,
  setQrCodeModalVisible,
  onEditAmount,
  receiveFiatAmount,
  lightningInvoice,
}: {
  qrCodeModalVisible: boolean;
  setQrCodeModalVisible: React.Dispatch<React.SetStateAction<boolean>>;
  onEditAmount: () => void;
  receiveFiatAmount: string;
  lightningInvoice?: string | null;
}) {
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const handleClickOutside = (event: MouseEvent) => {
      if (ref.current && !ref.current.contains(event.target as Node)) {
        setQrCodeModalVisible(false);
      }
    };
    document.addEventListener("mousedown", handleClickOutside);
    return () => {
      document.removeEventListener("mousedown", handleClickOutside);
    };
  }, [setQrCodeModalVisible]);

  return (
    <div className="mt-4 flex w-full flex-col items-center">
      <ReceiveQRCard>
        <div className="items-right flex h-full w-full flex-col justify-between rounded-2xl">
          <WalletIcon className="m-6 w-4" />
          <div className="m-6 text-[12px] text-[#f9f9f9] opacity-50">
            Powered by Spark
          </div>
        </div>
        <div className="flex h-full w-full items-center rounded-2xl">
          <div
            className="m-3 flex h-[160px] w-[160px] items-center justify-center rounded-xl"
            style={{ backgroundColor: "rgba(33, 43, 55, 0.6)" }}
            onClick={() => {
              setQrCodeModalVisible(true);
            }}
          >
            <QRCodeSVG
              value={lightningInvoice || ""}
              size={130}
              // fix the logo
              // imageSettings={{
              //   src: "../images/sparklogo.svg",
              //   height: 20,
              //   width: 20,
              //   excavate: true,
              // }}
            />
          </div>
        </div>
      </ReceiveQRCard>
      <ReceiveDetailsContainer>
        <DetailsRow
          title="Amount"
          subtitle={receiveFiatAmount}
          logoRight={<PencilIcon />}
          onClick={onEditAmount}
        />
        <DetailsRow
          borderTop={true}
          title="Lightning Invoice"
          subtitle={lightningInvoice}
          logoRight={<CopyIcon />}
          onClick={() => {
            navigator.clipboard.writeText(lightningInvoice || "");
            alert("Copied to clipboard");
          }}
        />
        <DetailsRow
          logoLeft={<ClockIcon />}
          borderTop={true}
          subtitle="Expires in 5 hours"
        />
      </ReceiveDetailsContainer>
      {qrCodeModalVisible && (
        <div className="fixed inset-0 flex items-center justify-center bg-black bg-opacity-50">
          <div
            className="relative flex flex-col items-center justify-center"
            ref={ref}
          >
            <div
              className="absolute left-2 top-[-26px] cursor-pointer"
              onClick={() => setQrCodeModalVisible(false)}
            >
              <CloseIcon strokeWidth="2" />
            </div>
            <div className="relative rounded-lg bg-white p-4">
              <QRCodeSVG value={lightningInvoice || ""} size={300} />
            </div>
            <div
              className="mt-4 flex h-[40px] max-w-[340px] flex-row items-center justify-center rounded-lg bg-[#10151C]"
              onClick={(e) => {
                e.stopPropagation();
                navigator.clipboard.writeText(lightningInvoice || "");
                alert("Copied to clipboard");
              }}
            >
              <div className="m-6 overflow-hidden text-ellipsis whitespace-nowrap text-[12px] text-[#f9f9f9] opacity-50">
                {lightningInvoice}
              </div>
              <div className="mr-5">
                <CopyIcon stroke="#7C7C7C" strokeWidth="1.5" />
              </div>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}

const ReceiveDetailsContainer = styled.div`
  margin-top: 8px;
  width: 100%;
  display: flex;
  flex-direction: column;
  border-radius: 16px;
  border: 0.33px solid #2d3845;
`;
const ReceiveQRCard = styled.div`
  width: 100%;
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

  box-shadow:
    0px 216px 60px 0px rgba(0, 0, 0, 0),
    0px 138px 55px 0px rgba(0, 0, 0, 0.01),
    0px 78px 47px 0px rgba(0, 0, 0, 0.05),
    0px 35px 35px 0px rgba(0, 0, 0, 0.09),
    0px 9px 19px 0px rgba(0, 0, 0, 0.1);
`;
