import { useEffect, useRef, useState } from "react";
import WalletIcon from "../icons/WalletIcon";
import styled from "styled-components";
import ClockIcon from "../icons/ClockIcon";
import CopyIcon from "../icons/CopyIcon";
import PencilIcon from "../icons/PencilIcon";
import DetailsRow from "./DetailsRow";
import { QRCodeSVG } from "qrcode.react";
import CloseIcon from "../icons/CloseIcon";

type ActiveButton = "bitcoin" | "lightning" | "uma";

const ACTIVE_BUTTON_STYLES =
  "rounded-[6px] border border-[rgba(249,249,249,0.12)] bg-[#0E3154] shadow-[0px_4px_6px_0px_rgba(0,0,0,0.14),0px_0px_0px_1px_#0C0D0F,0px_9px_14px_-5px_rgba(255,255,255,0.10)_inset]";

const INACTIVE_BUTTON_STYLES =
  "rounded-[6px] border border-transparent bg-transparent shadow-none";

export default function ReceiveDetails({
  qrCodeModalVisible,
  setQrCodeModalVisible,
  onEditAmount,
  receiveAmount,
}: {
  qrCodeModalVisible: boolean;
  setQrCodeModalVisible: React.Dispatch<React.SetStateAction<boolean>>;
  onEditAmount: () => void;
  receiveAmount: string;
}) {
  const [active, setActive] = useState<ActiveButton>("lightning");
  const [url, setUrl] = useState(
    "lnbcrt1990n1pnm02c4pp5uynyjcwx0a0p35wwrpffslxy5whgf8c4ld2xryv765xk6ernaqysdqqcqzpgxqyz5vqrzjqfd7grknq8s8hyl2c466ypdt48u0kd2gngragyjuppnyha0pj8jnuqqqqzav4wzeggqqqqqqqqqqqqqq9qsp5vyhdvum9yuud6ddqssrtfvelxfq8vw8hsut2fay6ju87rppycg5s9qxpqysgq7265qzsf2k5s9s3ptw85hr2xg3vx9v6lwj7y3cv3amnevl9e8cd4me55crj9rqmyt39p7nqyljnq9zkdsks9fsta029x6kzcg396kkgp4lg658"
  );

  const ref = useRef<HTMLDivElement>(null);

  const handleClickOutside = (event: MouseEvent) => {
    if (ref.current && !ref.current.contains(event.target as Node)) {
      setQrCodeModalVisible(false);
    }
  };

  useEffect(() => {
    document.addEventListener("mousedown", handleClickOutside);
    return () => {
      document.removeEventListener("mousedown", handleClickOutside);
    };
  }, []);

  return (
    <div className="flex flex-col items-center mt-4 w-full">
      {/* <div className="flex py-4 w-full">
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
      </div> */}
      <ReceiveQRCard>
        <div className="w-full h-full rounded-2xl flex flex-col items-right justify-between">
          <WalletIcon className="m-6 w-4" />
          <div className="text-[12px] text-[#f9f9f9] opacity-50 m-6">
            Powered by Spark
          </div>
        </div>
        <div className="flex items-center w-full h-full rounded-2xl">
          <div
            className="m-3 w-[160px] h-[160px] rounded-xl flex items-center justify-center"
            style={{ backgroundColor: "rgba(33, 43, 55, 0.6)" }}
            onClick={() => {
              setQrCodeModalVisible(true);
            }}
          >
            <QRCodeSVG
              value={url}
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
          subtitle={receiveAmount}
          logoRight={<PencilIcon />}
          onClick={onEditAmount}
        />
        <DetailsRow
          borderTop={true}
          title="Lightning Invoice"
          subtitle={url}
          logoRight={<CopyIcon />}
          onClick={() => {
            navigator.clipboard.writeText(url);
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
        <div className="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center">
          <div
            className="relative flex flex-col items-center justify-center"
            ref={ref}
          >
            <div
              className="absolute top-[-26px] left-2 cursor-pointer"
              onClick={() => setQrCodeModalVisible(false)}
            >
              <CloseIcon strokeWidth="2" />
            </div>
            <div className="relative bg-white p-4 rounded-lg">
              <QRCodeSVG value={url} size={300} />
            </div>
            <div
              className="flex flex-row items-center h-[40px] mt-4 bg-[#10151C] rounded-lg justify-center max-w-[340px]"
              onClick={(e) => {
                e.stopPropagation();
                navigator.clipboard.writeText(url);
                alert("Copied to clipboard");
              }}
            >
              <div className="text-[12px] text-[#f9f9f9] opacity-50 m-6 overflow-hidden text-ellipsis whitespace-nowrap">
                {url}
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

  box-shadow: 0px 216px 60px 0px rgba(0, 0, 0, 0),
    0px 138px 55px 0px rgba(0, 0, 0, 0.01),
    0px 78px 47px 0px rgba(0, 0, 0, 0.05), 0px 35px 35px 0px rgba(0, 0, 0, 0.09),
    0px 9px 19px 0px rgba(0, 0, 0, 0.1);
`;
