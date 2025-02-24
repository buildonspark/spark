import { QRCodeSVG } from "qrcode.react";
import { useEffect, useRef } from "react";
import styled from "styled-components";
import ClockIcon from "../icons/ClockIcon";
import CloseIcon from "../icons/CloseIcon";
import CopyIcon from "../icons/CopyIcon";
import PencilIcon from "../icons/PencilIcon";
import WalletIcon from "../icons/WalletIcon";
import { useWallet } from "../store/wallet";
import { CurrencyType } from "../utils/currency";
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
  const { activeInputCurrency, satsUsdPrice, activeAsset } = useWallet();
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
  console.log(activeInputCurrency.type);
  const receiveFiatAmount =
    activeInputCurrency.type === CurrencyType.FIAT
      ? inputAmount
      : (Number(inputAmount) * satsUsdPrice.value).toFixed(2);
  const receiveAssetAmount =
    activeInputCurrency.type === CurrencyType.FIAT
      ? (Number(inputAmount) / satsUsdPrice.value).toFixed(0)
      : Number(inputAmount).toFixed(0);

  return (
    <div className="mb-8 flex w-full flex-col items-center">
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
          subtitle={`$${Number(receiveFiatAmount.split(".")[0]).toLocaleString()}${receiveFiatAmount.split(".")[1] ? `.${receiveFiatAmount.split(".")[1]}` : ".00"} (${Number(receiveAssetAmount).toLocaleString()} ${activeAsset.code === "BTC" ? "SATs" : activeAsset.code || ""})`}
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
        <div className="fixed inset-0 flex h-full items-center justify-center bg-black bg-opacity-50">
          <div
            className="flex h-[510px] w-[360px] flex-col rounded-3xl bg-[#0E3154]"
            ref={ref}
          >
            <div
              className="flex h-[64px] w-full items-start justify-end pb-4 pt-5"
              onClick={() => setQrCodeModalVisible(false)}
            >
              <div className="mr-4 cursor-pointer rounded-full bg-[rgba(255,255,255,0.04)] p-2">
                <CloseIcon width="12" height="12" />
              </div>
            </div>
            <div className="h-[351px] w-full px-12 pb-10 pt-2">
              <div className="flex h-[250px] w-full items-center justify-center rounded-lg bg-[rgba(255,255,255,0.04)] p-6">
                <QRCodeSVG value={lightningInvoice || ""} size={250} />
              </div>
              <div
                className="mt-4 flex h-[40px] cursor-pointer flex-row items-center justify-between rounded-lg bg-[rgba(255,255,255,0.04)]"
                onClick={(e) => {
                  e.stopPropagation();
                  navigator.clipboard.writeText(lightningInvoice || "");
                  alert("Copied to clipboard");
                }}
              >
                <div className="m-6 overflow-hidden text-ellipsis whitespace-nowrap text-[12px] text-[#FFFFFF]">
                  {lightningInvoice}
                </div>
                <div className="mr-5">
                  <CopyIcon stroke="#FFFFFF" strokeWidth="1.5" />
                </div>
              </div>
            </div>
            <div className="flex h-[100px] w-full flex-col items-center justify-center bg-[rgba(255,255,255,0.04)] py-6">
              <span className="text-[20px] font-bold text-[#FFFFFF]">
                ${Number(receiveFiatAmount.split(".")[0]).toLocaleString()}
                {receiveFiatAmount.split(".")[1]
                  ? `.${receiveFiatAmount.split(".")[1]}`
                  : ".00"}{" "}
              </span>
              <span className="text-[12px] text-[#FFFFFF]">
                {Number(Number(receiveAssetAmount).toFixed(0)).toLocaleString()}{" "}
                {activeAsset.code === "BTC" ? "SATs" : activeAsset.code}
              </span>
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
  border: 0.5px solid rgba(249, 249, 249, 0.15);
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
