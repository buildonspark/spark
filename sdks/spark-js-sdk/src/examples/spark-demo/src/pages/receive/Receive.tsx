import { useCallback, useMemo, useState } from "react";
import CardForm from "../../components/CardForm";
import Networks, { Network } from "../../components/Networks";
import ArrowLeft from "../../icons/ArrowLeft";
import AmountInput from "../../components/AmountInput";
import { useNavigate } from "react-router-dom";
import ReceiveDetails from "../../components/ReceiveDetails";

enum ReceiveStep {
  NetworkSelect = "NetworkSelect",
  InputAmount = "InputAmount",
  ShareQuote = "ShareQuote",
  Success = "Success",
  Failed = "Failed",
}

export default function Receive() {
  const [amount, setAmount] = useState("0");
  const [paymentNetwork, setPaymentNetwork] = useState<Network>(Network.NONE);
  const [currentStep, setCurrentStep] = useState<ReceiveStep>(
    ReceiveStep.NetworkSelect
  );
  const navigate = useNavigate();

  const onSubmit = useCallback(() => {
    switch (currentStep) {
      case ReceiveStep.NetworkSelect:
        setCurrentStep(ReceiveStep.InputAmount);
        break;
      case ReceiveStep.InputAmount:
        setCurrentStep(ReceiveStep.ShareQuote);
        break;
      case ReceiveStep.ShareQuote:
        alert("Share quote");
        break;
    }
  }, [currentStep, navigate, setCurrentStep]);

  const onLogoLeftClick = useCallback(() => {
    switch (currentStep) {
      case ReceiveStep.ShareQuote:
        setCurrentStep(ReceiveStep.InputAmount);
        break;
      case ReceiveStep.InputAmount:
        setAmount("0");
        setCurrentStep(ReceiveStep.NetworkSelect);
        break;
      case ReceiveStep.NetworkSelect:
        navigate("/wallet");
        break;
    }
  }, [currentStep, navigate, setAmount, setCurrentStep]);

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

  return (
    <div>
      <CardForm
        topTitle={topTitle}
        logoLeft={<ArrowLeft strokeWidth="1.5" />}
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
          <AmountInput amount={amount} setAmount={setAmount} />
        )}
        {currentStep === ReceiveStep.ShareQuote && <ReceiveDetails />}
      </CardForm>
    </div>
  );
}
