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
import { Routes } from "../../routes";
import { PERMANENT_CURRENCIES, useWallet } from "../../store/wallet";
import { CurrencyType } from "../../utils/currency";
import { delay } from "../../utils/utils";
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
  const [primaryButtonLoading, setPrimaryButtonLoading] =
    useState<boolean>(false);
  const [sendResponse, setSendResponse] = useState<Transfer | string | null>(
    null,
  );
  const {
    satsUsdPrice,
    activeInputCurrency,
    sendTransfer,
    payLightningInvoice,
    withdrawToBtc,
    setActiveAsset,
    getMasterPublicKey,
  } = useWallet();

  const logoLeft = useMemo(() => {
    switch (currentStep) {
      case SendStep.Failed:
      case SendStep.Success:
        return null;
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
        setActiveAsset(PERMANENT_CURRENCIES.BTC);
        navigate(Routes.Wallet);
        break;
    }
  }, [currentStep, navigate, setCurrentStep, setActiveAsset]);

  const topTitle = useMemo(() => {
    switch (currentStep) {
      case SendStep.AddressInput:
        return "Send";
      case SendStep.AmountInput:
        return "Enter amount";
      case SendStep.ConfirmQuote:
        return "Send BTC";
      default:
        return "";
    }
  }, [currentStep]);

  const primaryButtonText = useMemo(() => {
    switch (currentStep) {
      case SendStep.AmountInput:
        return "Continue";
      case SendStep.ConfirmQuote:
        return "Send";
      case SendStep.Success:
        return "Done";
      case SendStep.Failed:
        return "Try again";
      default:
        return "Continue";
    }
  }, [currentStep]);

  const onPrimaryButtonClick = useCallback(async () => {
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

        setPrimaryButtonLoading(true);
        await delay(3000);
        setPrimaryButtonLoading(false);

        console.log("satsToSend", satsToSend);
        if (sendAddressNetwork === Network.LIGHTNING) {
          await payLightningInvoice(sendAddress);
        } else if (sendAddressNetwork === Network.SPARK) {
          await sendTransfer(satsToSend, sendAddress);
        } else if (sendAddressNetwork === Network.BITCOIN) {
          await withdrawToBtc(sendAddress, satsToSend);
        } else if (sendAddressNetwork === Network.PHONE) {
          const response = await fetch(
            `https://api.dev.dev.sparkinfra.net/graphql/spark/rc`,
            {
              method: "POST",
              headers: {
                "Content-Type": "application/json",
                "Spark-Identity-Public-Key": await getMasterPublicKey(),
              },
              body: JSON.stringify({
                query: `
                mutation GetPublicKey($phone: String!) {
                  wallet_user_identity_public_key(input: { phone_number: $phone }) {
                    identity_public_key
                  }
                }
              `,
                variables: {
                  phone: sendAddress,
                },
              }),
            },
          );
          const data = await response.json();
          const publicKey =
            data.data.wallet_user_identity_public_key.identity_public_key;

          await sendTransfer(satsToSend, publicKey);

          await fetch(`https://api.dev.dev.sparkinfra.net/graphql/spark/rc`, {
            method: "POST",
            headers: {
              "Content-Type": "application/json",
              "Spark-Identity-Public-Key": await getMasterPublicKey(),
            },
            body: JSON.stringify({
              query: `
              mutation NotifyReceiver($phone: String!, $amount: Long!) {
                notify_receiver_transfer(input: { 
                  phone_number: $phone,
                  amount_sats: $amount
                })
              }
            `,
              variables: {
                phone: sendAddress,
                amount: satsToSend,
              },
            }),
          });
        }
        // TODO: IF FAIL
        // setCurrentStep(SendStep.Failed);
        setCurrentStep(SendStep.Success);
        break;
      case SendStep.Failed:
        // TODO: TRY AGAIN functionality
        break;
      case SendStep.Success:
      default:
        setActiveAsset(PERMANENT_CURRENCIES.BTC);
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
    setActiveAsset,
    getMasterPublicKey,
    sendAddress,
    sendTransfer,
  ]);

  const secondaryButtonText = useMemo(() => {
    switch (currentStep) {
      case SendStep.Failed:
      default:
        return "Cancel";
    }
  }, [currentStep]);

  const onSecondaryButtonClick = useCallback(() => {
    switch (currentStep) {
      case SendStep.Failed:
      default:
        setActiveAsset(PERMANENT_CURRENCIES.BTC);
        navigate(Routes.Wallet);
        break;
    }
  }, [currentStep, navigate, setActiveAsset]);

  return (
    <CardForm
      headerDisabled={
        currentStep === SendStep.Success || currentStep === SendStep.Failed
      }
      topTitle={topTitle}
      primaryButtonDisabled={currentStep === SendStep.AddressInput}
      primaryButtonClick={onPrimaryButtonClick}
      primaryButtonLoading={primaryButtonLoading}
      primaryButtonText={primaryButtonText}
      secondaryButtonDisabled={currentStep !== SendStep.Failed}
      secondaryButtonText={secondaryButtonText}
      secondaryButtonClick={onSecondaryButtonClick}
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
        <SendDetails
          inputAmount={rawInputAmount}
          sendAddress={sendAddress}
          success={true}
        />
      )}
      {currentStep === SendStep.Failed && (
        <SendDetails
          inputAmount={rawInputAmount}
          sendAddress={sendAddress}
          success={false}
        />
      )}
    </CardForm>
  );
}
