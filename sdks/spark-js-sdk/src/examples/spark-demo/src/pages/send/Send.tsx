import { useCallback, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import { Transfer } from "../../../../../../dist/proto/spark";
import AddressInput from "../../components/AddressInput";
import AmountInput from "../../components/AmountInput";
import CardForm from "../../components/CardForm";
import ConfirmQuote from "../../components/ConfirmQuote";
import { Network } from "../../components/Networks";
import SendDetails from "../../components/SendDetails";
import ArrowLeft from "../../icons/ArrowLeft";
import CloseIcon from "../../icons/CloseIcon";
import { Routes } from "../../routes";
import { useWallet } from "../../store/wallet";
import { CurrencyType } from "../../utils/currency";

export enum SendStep {
  AddressInput = "AddressInput",
  AmountInput = "AmountInput",
  ConfirmQuote = "ConfirmQuote",
  Success = "Success",
  Failed = "Failed",
}

export default function Send() {
  const navigate = useNavigate();
  const [currentStep, setCurrentStep] = useState<SendStep>(
    SendStep.AddressInput,
  );
  const [sendAddress, setSendAddress] = useState<string>("");
  const [sendAddressNetwork, setSendAddressNetwork] = useState<Network>(
    Network.NONE,
  );
  const [rawInputAmount, setRawInputAmount] = useState<string>("0");
  const [sendResponse, setSendResponse] = useState<Transfer | string | null>(
    null,
  );
  const {
    satsUsdPrice,
    activeInputCurrency,
    sendTransfer,
    payLightningInvoice,
  } = useWallet();

  const logoLeft = useMemo(() => {
    switch (currentStep) {
      case SendStep.Success:
        return <CloseIcon />;
      default:
        return <ArrowLeft />;
    }
  }, [currentStep]);

  const onLogoLeftClick = useCallback(() => {
    switch (currentStep) {
      case SendStep.AmountInput:
        setRawInputAmount("0");
        setCurrentStep(SendStep.AddressInput);
        break;
      case SendStep.ConfirmQuote:
        setCurrentStep(SendStep.AmountInput);
        break;
      default:
        navigate(Routes.Wallet);
        break;
    }
  }, [currentStep, navigate, setCurrentStep]);

  const topTitle = useMemo(() => {
    switch (currentStep) {
      case SendStep.AddressInput:
        return "Send";
      case SendStep.AmountInput:
        return "Amount to send";
      case SendStep.ConfirmQuote:
        return "Send";
      default:
        return "";
    }
  }, [currentStep]);

  const submitButtonText = useMemo(() => {
    switch (currentStep) {
      case SendStep.AmountInput:
        return "Preview";
      case SendStep.ConfirmQuote:
        return "Send";
      case SendStep.Success:
        return "Done";
      default:
        return "Continue";
    }
  }, [currentStep]);

  const onSubmit = useCallback(async () => {
    switch (currentStep) {
      case SendStep.AddressInput:
        setCurrentStep(SendStep.AmountInput);
        break;
      case SendStep.AmountInput:
        setCurrentStep(SendStep.ConfirmQuote);
        break;
      case SendStep.ConfirmQuote:
        const satsToSend =
          activeInputCurrency.type === CurrencyType.FIAT
            ? Math.floor(Number(rawInputAmount) / satsUsdPrice.value)
            : Number(rawInputAmount);
        console.log("satsToSend", satsToSend);
        if (sendAddressNetwork === Network.LIGHTNING) {
          // await payLightningInvoice(sendAddress);
        } else if (sendAddressNetwork === Network.SPARK) {
          // await sendTransfer(satsToSend, sendAddress);
        } else if (sendAddressNetwork === Network.BITCOIN) {
          // TODO
        }
        setCurrentStep(SendStep.Success);
        break;
      case SendStep.Success:
        navigate(Routes.Wallet);
        break;
    }
  }, [
    currentStep,
    navigate,
    sendAddressNetwork,
    rawInputAmount,
    activeInputCurrency,
    satsUsdPrice,
  ]);

  return (
    <CardForm
      topTitle={topTitle}
      submitDisabled={currentStep === SendStep.AddressInput}
      onSubmit={onSubmit}
      submitButtonText={submitButtonText}
      logoLeft={logoLeft}
      logoLeftClick={onLogoLeftClick}
    >
      {currentStep === SendStep.AddressInput && (
        <AddressInput
          onAddressSelect={(address, addressNetwork) => {
            setSendAddress(address);
            setSendAddressNetwork(addressNetwork);
            setCurrentStep(SendStep.AmountInput);
          }}
        />
      )}
      {currentStep === SendStep.AmountInput && (
        <AmountInput
          rawInputAmount={rawInputAmount}
          setRawInputAmount={setRawInputAmount}
        />
      )}
      {currentStep === SendStep.ConfirmQuote && (
        <ConfirmQuote
          inputAmount={rawInputAmount}
          sendAddress={sendAddress}
          sendAddressNetwork={sendAddressNetwork}
        />
      )}
      {currentStep === SendStep.Success && (
        <SendDetails inputAmount={rawInputAmount} sendAddress={sendAddress} />
      )}
    </CardForm>
  );
}
