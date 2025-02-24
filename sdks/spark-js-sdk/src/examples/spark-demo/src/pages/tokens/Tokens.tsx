import { useCallback, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import AddressInput from "../../components/AddressInput";
import AmountInput from "../../components/AmountInput";
import CardForm from "../../components/CardForm";
import ConfirmQuote from "../../components/ConfirmQuote";
import CurrencyBalanceDetails from "../../components/CurrencyBalanceDetails";
import SendDetails from "../../components/SendDetails";
import TokenDetails from "../../components/TokenDetails";
import ArrowLeft from "../../icons/ArrowLeft";
import CloseIcon from "../../icons/CloseIcon";
import { Routes } from "../../routes";
import { PERMANENT_CURRENCIES, useWallet } from "../../store/wallet";
import { CurrencyType } from "../../utils/currency";

export enum TokensStep {
  SelectToken = "SelectToken",
  TokenDetails = "TokenDetails",
  SendTokenAddressInput = "SendTokenAddressInput",
  SendTokenInput = "SendTokenInput",
  SendTokenConfirmQuote = "SendTokenConfirmQuote",
  SendTokenSuccess = "SendTokenSuccess",
  SendTokenFailed = "SendTokenFailed",
}

export default function Tokens() {
  const [rawInputAmount, setRawInputAmount] = useState<string>("");
  const [currentStep, setCurrentStep] = useState<TokensStep>(
    TokensStep.SelectToken,
  );
  const [sendTokenAddress, setSendTokenAddress] = useState<string>("");
  const [sendTokenAddressNetwork, setSendTokenAddressNetwork] =
    useState<string>("");
  const {
    assets,
    activeAsset,
    activeInputCurrency,
    satsUsdPrice,
    setActiveAsset,
    setActiveInputCurrency,
  } = useWallet();
  const navigate = useNavigate();

  console.log(currentStep, activeAsset.name);

  const logoLeftClickHandler = useCallback(() => {
    switch (currentStep) {
      case TokensStep.SelectToken:
        setActiveAsset(PERMANENT_CURRENCIES.BTC);
        navigate(Routes.Wallet);
        break;
      case TokensStep.TokenDetails:
        setActiveAsset(PERMANENT_CURRENCIES.BTC);
        setCurrentStep(TokensStep.SelectToken);
        break;
      case TokensStep.SendTokenAddressInput:
        setCurrentStep(TokensStep.TokenDetails);
        break;
      case TokensStep.SendTokenInput:
        setCurrentStep(TokensStep.TokenDetails);
        break;
      case TokensStep.SendTokenConfirmQuote:
        setCurrentStep(TokensStep.SendTokenInput);
        break;
      case TokensStep.SendTokenSuccess:
      default:
        setActiveAsset(PERMANENT_CURRENCIES.BTC);
        navigate(Routes.Wallet);
        break;
    }
  }, [currentStep, setActiveAsset, setCurrentStep, navigate]);

  const submitButtonHandler = useCallback(() => {
    switch (currentStep) {
      case TokensStep.SendTokenInput:
        setCurrentStep(TokensStep.SendTokenConfirmQuote);
        break;
      case TokensStep.SendTokenConfirmQuote:
        setCurrentStep(TokensStep.SendTokenSuccess);
        break;
      default:
        navigate(Routes.Wallet);
        break;
    }
  }, [currentStep, navigate, setCurrentStep]);

  const submitButtonText = useMemo(() => {
    switch (currentStep) {
      case TokensStep.SendTokenInput:
        return "Preview";
      case TokensStep.SendTokenConfirmQuote:
        return "Send";
      case TokensStep.SendTokenSuccess:
        return "Done";
      default:
        return "Add Token";
    }
  }, [currentStep]);

  const topTitle = useMemo(() => {
    switch (currentStep) {
      case TokensStep.SelectToken:
        return "Select token";
      case TokensStep.TokenDetails:
        return "Token balance";
      case TokensStep.SendTokenAddressInput:
        return "Send address";
      case TokensStep.SendTokenInput:
        return "Send amount";
      case TokensStep.SendTokenConfirmQuote:
        return "Confirm transaction";
      case TokensStep.SendTokenSuccess:
        return "Success";
      default:
        return "Send";
    }
  }, [currentStep]);

  const logoRight = useMemo(() => {
    switch (currentStep) {
      case TokensStep.SendTokenInput:
      case TokensStep.SendTokenAddressInput:
      case TokensStep.TokenDetails:
      case TokensStep.SendTokenConfirmQuote:
      case TokensStep.SendTokenSuccess:
        return activeAsset.logo;
      default:
        return null;
    }
  }, [currentStep, activeAsset]);
  return (
    <CardForm
      topTitle={topTitle}
      onSubmit={submitButtonHandler}
      submitButtonText={submitButtonText}
      logoLeft={
        currentStep === TokensStep.SendTokenSuccess ? (
          <CloseIcon />
        ) : (
          <ArrowLeft />
        )
      }
      logoLeftClick={logoLeftClickHandler}
      logoRight={logoRight}
      submitDisabled={
        currentStep === TokensStep.TokenDetails ||
        currentStep === TokensStep.SelectToken ||
        currentStep === TokensStep.SendTokenAddressInput
      }
    >
      {currentStep === TokensStep.SelectToken &&
        Object.values(assets)
          .filter((asset) => asset.type === CurrencyType.TOKEN)
          .map((token) => {
            return (
              <CurrencyBalanceDetails
                key={token.name}
                logo={token.logo}
                currency={token.name}
                fiatBalance={token.balance?.toString() ?? "0"}
                onClick={() => {
                  setActiveAsset(token);
                  setCurrentStep(TokensStep.TokenDetails);
                }}
              />
            );
          })}
      {currentStep === TokensStep.TokenDetails && (
        <TokenDetails
          onSendButtonClick={() => {
            setActiveInputCurrency(activeAsset);
            setCurrentStep(TokensStep.SendTokenAddressInput);
          }}
        />
      )}
      {currentStep === TokensStep.SendTokenAddressInput && (
        <AddressInput
          onAddressSelect={(address, addressNetwork) => {
            setSendTokenAddress(address);
            setSendTokenAddressNetwork(addressNetwork);
            setCurrentStep(TokensStep.SendTokenInput);
          }}
        />
      )}
      {currentStep === TokensStep.SendTokenInput && (
        <AmountInput
          rawInputAmount={rawInputAmount}
          setRawInputAmount={setRawInputAmount}
        />
      )}
      {currentStep === TokensStep.SendTokenConfirmQuote && (
        <ConfirmQuote
          inputAmount={rawInputAmount}
          sendAddress={sendTokenAddress}
          sendAddressNetwork={sendTokenAddressNetwork}
        />
      )}
      {currentStep === TokensStep.SendTokenSuccess && (
        <SendDetails
          inputAmount={rawInputAmount}
          sendAddress={sendTokenAddress}
        />
      )}
    </CardForm>
  );
}
