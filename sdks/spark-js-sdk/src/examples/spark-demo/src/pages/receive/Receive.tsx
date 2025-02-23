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
import { Currency } from "../../utils/currency";
import SparkDepositAddress from "./SparkDepositAddress";

enum ReceiveStep {
  NetworkSelect = "NetworkSelect",
  InputAmount = "InputAmount",
  ShareQuote = "ShareQuote",
  Success = "Success",
  Failed = "Failed",
  SparkDepositAddress = "SparkDepositAddress",
}

export default function Receive() {
  const [rawInputAmount, setRawInputAmount] = useState("0");
  const [lightningInvoice, setLightningInvoice] = useState<string | null>(null);
  const [paymentNetwork, setPaymentNetwork] = useState<Network>(Network.NONE);
  const [qrCodeModalVisible, setQrCodeModalVisible] = useState<boolean>(false);
  const [currentStep, setCurrentStep] = useState<ReceiveStep>(
    ReceiveStep.NetworkSelect,
  );
  const { satsUsdPrice, activeCurrency } = useWallet();
  const { createLightningInvoice } = useWallet();
  const navigate = useNavigate();

  const onSubmit = useCallback(async () => {
    switch (currentStep) {
      case ReceiveStep.NetworkSelect:
        setCurrentStep(ReceiveStep.InputAmount);
        break;
      case ReceiveStep.InputAmount:
        const satsToReceive =
          activeCurrency === Currency.USD
            ? Math.floor(Number(rawInputAmount) / satsUsdPrice.value)
            : Number(rawInputAmount);

        const invoice = await createLightningInvoice(
          satsToReceive,
          "test memo",
        );
        setLightningInvoice(invoice);
        setCurrentStep(ReceiveStep.ShareQuote);
        break;
      case ReceiveStep.ShareQuote:
        setQrCodeModalVisible(true);
        break;
    }
  }, [
    setCurrentStep,
    createLightningInvoice,
    activeCurrency,
    rawInputAmount,
    satsUsdPrice,
    currentStep,
  ]);

  const onLogoLeftClick = useCallback(() => {
    switch (currentStep) {
      case ReceiveStep.InputAmount:
        setRawInputAmount("0");
        setCurrentStep(ReceiveStep.NetworkSelect);
        break;
      case ReceiveStep.NetworkSelect:
      case ReceiveStep.ShareQuote:
        navigate(Routes.Wallet);
        break;
      case ReceiveStep.SparkDepositAddress:
        setCurrentStep(ReceiveStep.NetworkSelect);
        break;
    }
  }, [currentStep, navigate, setRawInputAmount, setCurrentStep]);

  const topTitle = useMemo(() => {
    switch (currentStep) {
      case ReceiveStep.Success:
        return "Receive money via";
      case ReceiveStep.InputAmount:
        return "Amount to receive";
      case ReceiveStep.ShareQuote:
        return "Receive";
      case ReceiveStep.SparkDepositAddress:
        return "Spark deposit address";
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

  const onSelectNetwork = useCallback((network: Network) => {
    setPaymentNetwork(network);
    if (network === Network.SPARK) {
      setCurrentStep(ReceiveStep.SparkDepositAddress);
    } else {
      setCurrentStep(ReceiveStep.InputAmount);
    }
  }, []);

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
        submitDisabled={
          currentStep === ReceiveStep.NetworkSelect ||
          currentStep === ReceiveStep.SparkDepositAddress
        }
      >
        {currentStep === ReceiveStep.NetworkSelect && (
          <Networks onSelectNetwork={onSelectNetwork} />
        )}
        {currentStep === ReceiveStep.SparkDepositAddress && (
          <SparkDepositAddress />
        )}
        {currentStep === ReceiveStep.InputAmount && (
          <AmountInput
            rawInputAmount={rawInputAmount}
            setRawInputAmount={setRawInputAmount}
          />
        )}
        {currentStep === ReceiveStep.ShareQuote && (
          <ReceiveDetails
            receiveFiatAmount={
              activeCurrency === Currency.USD
                ? rawInputAmount
                : `${Number(rawInputAmount) * satsUsdPrice.value}`
            }
            lightningInvoice={lightningInvoice}
            onEditAmount={() => {
              setCurrentStep(ReceiveStep.InputAmount);
            }}
            qrCodeModalVisible={qrCodeModalVisible}
            setQrCodeModalVisible={setQrCodeModalVisible}
          />
        )}
      </CardForm>
    </div>
  );
}
