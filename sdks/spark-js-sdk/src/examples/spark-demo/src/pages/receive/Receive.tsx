import { useCallback, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import AmountInput from "../../components/AmountInput";
import CardForm from "../../components/CardForm";
import Networks, { Network } from "../../components/Networks";
import ReceiveDetails from "../../components/ReceiveDetails";
import ArrowLeft from "../../icons/ArrowLeft";
import CloseIcon from "../../icons/CloseIcon";
import { Routes } from "../../routes";
import { useWallet } from "../../store/wallet";

enum ReceiveStep {
  NetworkSelect = "NetworkSelect",
  InputAmount = "InputAmount",
  ShareQuote = "ShareQuote",
  Success = "Success",
  Failed = "Failed",
}

export default function Receive() {
  const [fiatAmount, setFiatAmount] = useState("0");
  const [paymentNetwork, setPaymentNetwork] = useState<Network>(Network.NONE);
  const [qrCodeModalVisible, setQrCodeModalVisible] = useState<boolean>(false);
  const [currentStep, setCurrentStep] = useState<ReceiveStep>(
    ReceiveStep.NetworkSelect
  );
  const [invoice, setInvoice] = useState<string | null>(null);
  const navigate = useNavigate();

  const { btcPrice } = useWallet();

  const { createLightningInvoice } = useWallet();
  const onSubmit = useCallback(async () => {
    switch (currentStep) {
      case ReceiveStep.NetworkSelect:
        setCurrentStep(ReceiveStep.InputAmount);
        break;
      case ReceiveStep.InputAmount:
        const satsToSend = Math.floor(Number(fiatAmount) / btcPrice.value);
        console.log("satsToSend", satsToSend);
        const invoice = await createLightningInvoice(satsToSend, "test memo");
        setInvoice(invoice);
        setCurrentStep(ReceiveStep.ShareQuote);
        break;
      case ReceiveStep.ShareQuote:
        setQrCodeModalVisible(true);
        break;
    }
  }, [currentStep, setCurrentStep]);

  const onLogoLeftClick = useCallback(() => {
    switch (currentStep) {
      case ReceiveStep.InputAmount:
        setFiatAmount("0");
        setCurrentStep(ReceiveStep.NetworkSelect);
        break;
      case ReceiveStep.NetworkSelect:
      case ReceiveStep.ShareQuote:
        navigate(Routes.Wallet);
        break;
    }
  }, [currentStep, navigate, setFiatAmount, setCurrentStep]);

  const topTitle = useMemo(() => {
    switch (currentStep) {
      case ReceiveStep.Success:
        return "Receive money via";
      case ReceiveStep.InputAmount:
        return "Amount to receive";
      case ReceiveStep.ShareQuote:
        return "Receive";
      default:
        return "Receive money via";
    }
  }, [currentStep]);

  const logoLeft = useMemo(() => {
    switch (currentStep) {
      case ReceiveStep.ShareQuote:
        return <CloseIcon strokeWidth="1.5" />;
      default:
        return <ArrowLeft strokeWidth="1.5" />;
    }
  }, [currentStep]);

  return (
    <div>
      <CardForm
        topTitle={topTitle}
        logoLeft={logoLeft}
        onSubmit={onSubmit}
        submitButtonText={
          currentStep === ReceiveStep.InputAmount
            ? "Confirm"
            : currentStep === ReceiveStep.ShareQuote
            ? "Share"
            : ""
        }
        logoLeftClick={onLogoLeftClick}
        submitDisabled={currentStep === ReceiveStep.NetworkSelect}
      >
        {currentStep === ReceiveStep.NetworkSelect && (
          <Networks
            onSelectNetwork={(network) => {
              setPaymentNetwork(network);
              setCurrentStep(ReceiveStep.InputAmount);
            }}
          />
        )}
        {currentStep === ReceiveStep.InputAmount && (
          <AmountInput fiatAmount={fiatAmount} setFiatAmount={setFiatAmount} />
        )}
        {currentStep === ReceiveStep.ShareQuote && (
          <ReceiveDetails
            receiveFiatAmount={fiatAmount}
            onEditAmount={() => {
              setCurrentStep(ReceiveStep.InputAmount);
            }}
            qrCodeModalVisible={qrCodeModalVisible}
            setQrCodeModalVisible={setQrCodeModalVisible}
            invoice={invoice}
          />
        )}
      </CardForm>
    </div>
  );
}
