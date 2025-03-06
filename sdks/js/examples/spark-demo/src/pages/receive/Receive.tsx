import { useCallback, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import AmountInput from "../../components/AmountInput";
import CardForm from "../../components/CardForm";
import Networks, { Network } from "../../components/Networks";
import ReceiveDetails from "../../components/ReceiveDetails";
import ChevronIcon from "../../icons/ChevronIcon";
import { Routes } from "../../routes";
import { PERMANENT_CURRENCIES, useWallet } from "../../store/wallet";
import { CurrencyType } from "../../utils/currency";
import { delay } from "../../utils/utils";
import BitcoinDepositAddress from "./BitcoinDepositAddress";
import SparkDepositAddress from "./SparkDepositAddress";

enum ReceiveStep {
  NetworkSelect = "NetworkSelect",
  InputAmount = "InputAmount",
  ShareQuote = "ShareQuote",
  Success = "Success",
  Failed = "Failed",
  SparkDepositAddress = "SparkDepositAddress",
  BitcoinDepositAddress = "BitcoinDepositAddress",
}

export default function Receive() {
  const [rawInputAmount, setRawInputAmount] = useState("0");
  const [lightningInvoice, setLightningInvoice] = useState<string | null>(null);
  const [, setPaymentNetwork] = useState<Network>(Network.NONE);
  const [primaryButtonLoading, setPrimaryButtonLoading] =
    useState<boolean>(false);
  const [qrCodeModalVisible, setQrCodeModalVisible] = useState<boolean>(false);
  const [currentStep, setCurrentStep] = useState<ReceiveStep>(
    ReceiveStep.NetworkSelect,
  );
  const {
    satsUsdPrice,
    activeInputCurrency,
    createLightningInvoice,
    setActiveAsset,
  } = useWallet();
  const navigate = useNavigate();

  const onPrimaryButtonClick = useCallback(async () => {
    switch (currentStep) {
      case ReceiveStep.NetworkSelect:
        setCurrentStep(ReceiveStep.InputAmount);
        break;
      case ReceiveStep.InputAmount:
        const satsToReceive =
          activeInputCurrency.type === CurrencyType.FIAT
            ? Math.floor(Number(rawInputAmount) / satsUsdPrice.value)
            : Number(rawInputAmount);

        const invoice = await createLightningInvoice(
          satsToReceive,
          "test memo",
        );

        setPrimaryButtonLoading(true);
        await delay(3000);
        setPrimaryButtonLoading(false);

        setLightningInvoice(invoice);
        setCurrentStep(ReceiveStep.ShareQuote);
        break;
      case ReceiveStep.ShareQuote:
      default:
        setActiveAsset(PERMANENT_CURRENCIES.get("BTC")!);
        navigate(Routes.Wallet);
        break;
    }
  }, [
    activeInputCurrency,
    rawInputAmount,
    satsUsdPrice,
    currentStep,
    setCurrentStep,
    createLightningInvoice,
    setActiveAsset,
    navigate,
  ]);

  const onSecondaryButtonClick = useCallback(() => {
    switch (currentStep) {
      case ReceiveStep.ShareQuote:
        setQrCodeModalVisible(true);
        break;
      default:
        return null;
    }
  }, [currentStep, setQrCodeModalVisible]);

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
      case ReceiveStep.BitcoinDepositAddress:
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
      case ReceiveStep.SparkDepositAddress:
        return "Spark deposit address";
      case ReceiveStep.BitcoinDepositAddress:
        return "Bitcoin deposit address";
      default:
        return "Receive";
    }
  }, [currentStep]);

  const logoLeft = useMemo(() => {
    switch (currentStep) {
      case ReceiveStep.ShareQuote:
        return null;
      default:
        return (
          <ChevronIcon
            direction="left"
            opacity={1}
            height={24}
            width={24}
            strokeWidth={2}
            stroke="rgba(250, 250, 250, 0.80)"
          />
        );
    }
  }, [currentStep]);

  const onSelectNetwork = useCallback((network: Network) => {
    setPaymentNetwork(network);
    if (network === Network.SPARK) {
      setCurrentStep(ReceiveStep.SparkDepositAddress);
    } else if (network === Network.BITCOIN) {
      setCurrentStep(ReceiveStep.BitcoinDepositAddress);
    } else {
      setCurrentStep(ReceiveStep.InputAmount);
    }
  }, []);

  return (
    <div>
      <CardForm
        headerDisabled={currentStep === ReceiveStep.ShareQuote}
        topTitle={topTitle}
        logoLeft={logoLeft}
        logoLeftClick={onLogoLeftClick}
        primaryButtonDisabled={
          currentStep === ReceiveStep.NetworkSelect ||
          currentStep === ReceiveStep.SparkDepositAddress ||
          currentStep === ReceiveStep.BitcoinDepositAddress
        }
        primaryButtonLoading={primaryButtonLoading}
        primaryButtonClick={onPrimaryButtonClick}
        primaryButtonText={
          currentStep === ReceiveStep.InputAmount
            ? "Confirm"
            : currentStep === ReceiveStep.ShareQuote
              ? "Done"
              : ""
        }
        secondaryButtonDisabled={currentStep !== ReceiveStep.ShareQuote}
        secondaryButtonClick={onSecondaryButtonClick}
        secondaryButtonText={
          currentStep === ReceiveStep.ShareQuote ? "Share" : ""
        }
      >
        {currentStep === ReceiveStep.NetworkSelect && (
          <Networks onSelectNetwork={onSelectNetwork} />
        )}
        {currentStep === ReceiveStep.SparkDepositAddress && (
          <SparkDepositAddress />
        )}
        {currentStep === ReceiveStep.BitcoinDepositAddress && (
          <BitcoinDepositAddress />
        )}
        {currentStep === ReceiveStep.InputAmount && (
          <AmountInput
            rawInputAmount={rawInputAmount}
            setRawInputAmount={setRawInputAmount}
          />
        )}
        {currentStep === ReceiveStep.ShareQuote && (
          <ReceiveDetails
            inputAmount={rawInputAmount}
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
