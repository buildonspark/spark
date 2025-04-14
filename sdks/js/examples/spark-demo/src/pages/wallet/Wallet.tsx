import { Transfer } from "@buildonspark/spark-sdk/proto/spark";
import { bytesToHex } from "@noble/hashes/utils";
import NumberFlow, { NumberFlowGroup } from "@number-flow/react";
import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { toast } from "react-toastify";
import Button from "../../components/Button";
import CurrencyBalanceDetails from "../../components/CurrencyBalanceDetails";
import StyledContainer from "../../components/StyledContainer";
import TransactionDetailRow from "../../components/TransactionDetailRow";
import { usePaginatedTransfers } from "../../hooks/usePaginatedTransfers";
import CopyIcon from "../../icons/CopyIcon";
import StableCoinLogo from "../../icons/StableCoinLogo";
import { Routes } from "../../routes";
import { PERMANENT_CURRENCIES, useWallet } from "../../store/wallet";
import { formatAssetAmount, formatFiatAmount } from "../../utils/utils";
export default function Wallet() {
  const navigate = useNavigate();
  const notify = () => toast("Copied!");
  const { btcBalance, satsUsdPrice, getMasterPublicKey, isInitialized } =
    useWallet();

  const [pubkey, setPubkey] = useState("");
  const transfersQuery = usePaginatedTransfers({
    limit: 5,
    offset: 0,
  });
  useEffect(() => {
    if (isInitialized) {
      getMasterPublicKey().then((pubkey) => {
        if (pubkey) {
          setPubkey(pubkey);
        }
      });
    }
  }, [getMasterPublicKey, isInitialized]);

  const assetBalance = formatFiatAmount(
    btcBalance.value,
    satsUsdPrice.value,
    PERMANENT_CURRENCIES.get("USD")!,
    false,
  );

  const btcBalanceDisplay = formatAssetAmount(
    btcBalance.value,
    PERMANENT_CURRENCIES.get("BTC")!,
    true,
  );

  return (
    <div>
      <StyledContainer className="flex h-[220px] w-full flex-col items-center justify-center p-4">
        <div className="flex h-[40px] w-full flex-row items-center justify-end">
          <div
            className="flex cursor-pointer flex-row items-center justify-center"
            onClick={() => {
              navigator.clipboard.writeText(pubkey);
              notify();
            }}
          >
            <div className="flex max-w-[80px] flex-row items-center justify-center text-[13px] text-[#FAFAFA80]">
              <div className="mr-1 overflow-hidden text-ellipsis whitespace-nowrap">
                {pubkey ? pubkey : "Loading..."}
              </div>
            </div>
            <div className="flex h-4 w-4 items-center justify-center">
              <CopyIcon stroke="#FAFAFA80" />
            </div>
          </div>
        </div>
        <div className="flex h-[140px] w-full flex-col items-start justify-end">
          {btcBalance.isLoading ? (
            <div className="flex w-full flex-col space-y-3">
              <div className="h-10 w-[124px] animate-gradient-x rounded-md bg-[linear-gradient(90deg,#1A1A1A,#1A1A1A,#5A5A5A,#1A1A1A,#1A1A1A)] bg-[length:1000%_100%]"></div>
              <div className="h-[18px] w-[150px] animate-gradient-x rounded-md bg-[linear-gradient(90deg,#1A1A1A,#1A1A1A,#5A5A5A,#1A1A1A,#1A1A1A)] bg-[length:1000%_100%]"></div>
            </div>
          ) : (
            <NumberFlowGroup>
              <div className="text-[32px]">
                <NumberFlow
                  value={assetBalance.amount}
                  suffix={assetBalance.code}
                />
                <span className="text-[15px] text-[#FAFAFA]">{`${" USD"}`}</span>
              </div>
              <div className="text-[13px] text-[#FAFAFA80]">
                <NumberFlow
                  value={btcBalanceDisplay.amount}
                  suffix={btcBalanceDisplay.code}
                />
              </div>
            </NumberFlowGroup>
          )}
        </div>
        <div className="mt-6 flex w-full items-center justify-center gap-2">
          <Button
            text="Receive"
            kind="secondary"
            direction="vertical"
            onClick={() => {
              navigate(Routes.Receive);
            }}
            height={44}
          />
          <Button
            text="Send"
            kind="primary"
            direction="vertical"
            onClick={() => {
              navigate(Routes.Send);
            }}
            height={44}
          />
        </div>
      </StyledContainer>
      <div className="mt-8 w-full border-y border-[#f9f9f9] border-opacity-5">
        <CurrencyBalanceDetails
          logo={<StableCoinLogo strokeWidth="1.50" />}
          currency="Stablecoins"
          fiatBalance="$0.00"
          onClick={() => {
            navigate(Routes.Tokens);
          }}
        />
      </div>
      {(transfersQuery.isLoading ||
        !transfersQuery.data?.transfers ||
        transfersQuery.data?.transfers?.length === 0) && (
        <div className="mb-8 mt-12 flex flex-col items-center justify-center text-[15px]">
          <span>Your wallet activity starts now</span>
          <div className="mt-2 flex flex-col items-center justify-center text-[13px] text-[#F9F9F999]">
            <span>Add or receive BTC or stablecoins</span>
            <span>to your wallet to get started</span>
          </div>
        </div>
      )}
      {transfersQuery?.data?.transfers?.length &&
      transfersQuery?.data?.transfers?.length > 0 ? (
        <div className="mt-4">
          <div className="flex flex-row items-center justify-between p-2">
            <div className="text-[15px] font-medium text-[#F9F9F999]">
              Recent activity
            </div>
            <div
              className="cursor-pointer text-[13px] font-medium"
              onClick={() => {
                navigate(Routes.Transactions);
              }}
            >
              View all
            </div>
          </div>
          {transfersQuery?.data?.transfers?.map(
            (transfer: Transfer, index: number) => {
              if (index >= 3) return null;
              const receiver = bytesToHex(transfer.receiverIdentityPublicKey);
              if (receiver === pubkey) {
                return (
                  <TransactionDetailRow
                    key={`${index}`}
                    transactionType="receive"
                    asset={PERMANENT_CURRENCIES.get("BTC")!}
                    assetAmount={transfer.totalValue}
                    counterparty={bytesToHex(
                      transfer.receiverIdentityPublicKey,
                    )}
                  />
                );
              } else {
                return (
                  <TransactionDetailRow
                    key={`${index}`}
                    transactionType="send"
                    asset={PERMANENT_CURRENCIES.get("BTC")!}
                    assetAmount={transfer.totalValue}
                    counterparty={bytesToHex(transfer.senderIdentityPublicKey)}
                  />
                );
              }
            },
          )}
        </div>
      ) : null}
    </div>
  );
}
