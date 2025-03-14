import { QRCodeSVG } from "qrcode.react";
import { useEffect, useRef } from "react";
import { toast } from "react-toastify";
import styled from "styled-components";
import ClockIcon from "../icons/ClockIcon";
import CopyIcon from "../icons/CopyIcon";
import DetailsRow from "./DetailsRow";

export default function ReceiveDetails({
  qrCodeModalVisible,
  setQrCodeModalVisible,
  onEditAmount,
  inputAmount,
  lightningInvoice,
}: {
  qrCodeModalVisible: boolean;
  setQrCodeModalVisible: React.Dispatch<React.SetStateAction<boolean>>;
  onEditAmount: () => void;
  inputAmount: string;
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
  const notify = () => toast("Copied!");

  return (
    <div className="mb-8 flex w-full flex-col items-center">
      <div className="flex h-18xl w-full items-center justify-center rounded-2xl bg-white-6">
        <div
          className="m-3 flex h-[200px] w-[200px] items-center justify-center rounded-xl"
          style={{ backgroundColor: "rgba(33, 43, 55, 0.6)" }}
        >
          <QRCodeSVG
            value={lightningInvoice || ""}
            size={160}
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
      <ReceiveDetailsContainer>
        <DetailsRow
          title="Lightning Invoice"
          subtitle={lightningInvoice}
          logoRight={<CopyIcon />}
          onClick={() => {
            navigator.clipboard.writeText(lightningInvoice || "");
            notify();
          }}
        />
        <DetailsRow
          logoLeft={<ClockIcon />}
          borderTop={true}
          subtitle="Expires in 24 hours"
        />
      </ReceiveDetailsContainer>
    </div>
  );
}

const ReceiveDetailsContainer = styled.div`
  margin-top: 24px;
  width: 100%;
  display: flex;
  flex-direction: column;
  border-radius: 16px;
  border: 0.33px solid #2d3845;
`;
