import { useCallback, useEffect, useMemo, useState } from "react";
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
import { Currency, CurrencyType } from "../../utils/currency";

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
  const [sendTokenLoading, setSendTokenLoading] = useState(false);
  const [sendTokenAddress, setSendTokenAddress] = useState<string>("");
  const [sendTokenAddressNetwork, setSendTokenAddressNetwork] =
    useState<string>("");
  const {
    assets,
    activeAsset,
    activeInputCurrency,
    tokenBalances,
    updateAssets,
    transferTokens,
    setActiveAsset,
    setActiveInputCurrency,
  } = useWallet();
  const navigate = useNavigate();

  const handleTokenSend = useCallback(async () => {
    try {
      setSendTokenLoading(true);
      const assetsToSendValue: bigint =
        activeInputCurrency.type === CurrencyType.TOKEN
          ? BigInt(rawInputAmount)
          : BigInt(Number(rawInputAmount) * (activeAsset.usdPrice ?? 1));
      if (!activeAsset.pubkey) {
        throw new Error("Active asset pubkey is not set");
      }
      await transferTokens(
        activeAsset.pubkey,
        assetsToSendValue,
        sendTokenAddress,
      );
      setSendTokenLoading(false);
    } catch (e) {
      setSendTokenLoading(false);
      setCurrentStep(TokensStep.SendTokenFailed);
    }
    setCurrentStep(TokensStep.SendTokenSuccess);
  }, [
    setCurrentStep,
    transferTokens,
    activeAsset,
    rawInputAmount,
    sendTokenAddress,
    activeInputCurrency,
  ]);

  const logoLeftClickHandler = useCallback(async () => {
    switch (currentStep) {
      case TokensStep.SelectToken:
        setActiveAsset(PERMANENT_CURRENCIES.get("BTC")!);
        navigate(Routes.Wallet);
        break;
      case TokensStep.TokenDetails:
        setActiveAsset(PERMANENT_CURRENCIES.get("BTC")!);
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
        setActiveAsset(PERMANENT_CURRENCIES.get("BTC")!);
        navigate(Routes.Wallet);
        break;
    }
  }, [currentStep, setActiveAsset, setCurrentStep, navigate]);

  const onPrimaryButtonClick = useCallback(async () => {
    switch (currentStep) {
      case TokensStep.SendTokenInput:
        setCurrentStep(TokensStep.SendTokenConfirmQuote);
        break;
      case TokensStep.SendTokenConfirmQuote:
        await handleTokenSend();
        break;
      default:
        setActiveAsset(PERMANENT_CURRENCIES.get("BTC")!);
        navigate(Routes.Wallet);
        break;
    }
  }, [currentStep, navigate, setCurrentStep, setActiveAsset, handleTokenSend]);

  const secondaryButtonClickHandler = useCallback(async () => {
    switch (currentStep) {
      case TokensStep.SendTokenFailed:
        await handleTokenSend();
        break;
      default:
        break;
    }
  }, [handleTokenSend, currentStep]);

  const primaryButtonText = useMemo(() => {
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
        return `Send ${activeAsset.code}`;
      case TokensStep.SendTokenSuccess:
        return "Success";
      case TokensStep.SendTokenFailed:
        return "Failed";
      default:
        return "Send";
    }
  }, [currentStep, activeAsset]);

  const logoRight = useMemo(() => {
    switch (currentStep) {
      case TokensStep.SendTokenInput:
      case TokensStep.SendTokenAddressInput:
      case TokensStep.TokenDetails:
      case TokensStep.SendTokenConfirmQuote:
      case TokensStep.SendTokenSuccess:
      case TokensStep.SendTokenFailed:
        return activeAsset.logo;
      default:
        return null;
    }
  }, [currentStep, activeAsset]);

  useEffect(() => {
    let balances = tokenBalances.value as Map<
      string,
      { balance: bigint; leafCount: number }
    >;
    if (balances) {
      console.log("balances: ", balances);
      const newAssets: Map<string, Currency> = new Map();
      balances.forEach((value, key) => {
        newAssets.set(key, {
          name: key,
          decimals: 0,
          type: CurrencyType.TOKEN,
          balance: Number(value.balance),
          logo: null,
          pubkey: key,
          usdPrice: 1,
        });
      });
      const isDifferent = Array.from(newAssets.keys()).some(
        (key) =>
          !assets.get(key) ||
          newAssets.get(key)?.balance !== assets.get(key)?.balance,
      );
      if (isDifferent) {
        updateAssets(newAssets);
      }
    }
  }, []);

  return (
    <CardForm
      headerDisabled={
        currentStep === TokensStep.SendTokenSuccess ||
        currentStep === TokensStep.SendTokenFailed
      }
      topTitle={topTitle}
      primaryButtonLoading={sendTokenLoading}
      primaryButtonDisabled={
        currentStep === TokensStep.TokenDetails ||
        currentStep === TokensStep.SelectToken ||
        currentStep === TokensStep.SendTokenAddressInput
      }
      primaryButtonClick={onPrimaryButtonClick}
      primaryButtonText={primaryButtonText}
      secondaryButtonText={
        currentStep === TokensStep.SendTokenFailed ? "Retry" : undefined
      }
      secondaryButtonClick={secondaryButtonClickHandler}
      secondaryButtonLoading={sendTokenLoading}
      secondaryButtonDisabled={
        currentStep === TokensStep.SendTokenFailed ? false : true
      }
      logoLeft={
        currentStep === TokensStep.SendTokenSuccess ? (
          <CloseIcon />
        ) : (
          <ArrowLeft />
        )
      }
      logoLeftClick={logoLeftClickHandler}
      logoRight={logoRight}
    >
      {currentStep === TokensStep.SelectToken && (
        <div className="flex max-h-[420px] flex-col overflow-y-auto p-2">
          {(() => {
            const filteredAssets = Array.from(assets.values()).filter(
              (asset) => asset.type === CurrencyType.TOKEN,
            );

            if (filteredAssets.length === 0) {
              return (
                <div className="flex flex-col items-center justify-center p-6 text-center text-gray-500">
                  <p>No tokens found</p>
                  <p className="mt-4 text-sm">
                    Tokens will appear here once found
                  </p>
                </div>
              );
            }

            return filteredAssets.map((token) => (
              <CurrencyBalanceDetails
                key={token.name}
                logo={token.logo}
                currency={token.name}
                fiatBalance={token.balance?.toString() ?? "0"}
                onClick={() => {
                  setActiveAsset(token);
                  setCurrentStep(TokensStep.TokenDetails);
                }}
                logoBorderEnabled={false}
              />
            ));
          })()}
        </div>
      )}
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
          success={true}
        />
      )}
      {currentStep === TokensStep.SendTokenFailed && (
        <SendDetails
          inputAmount={rawInputAmount}
          sendAddress={sendTokenAddress}
          success={false}
        />
      )}
    </CardForm>
  );
}
