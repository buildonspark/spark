import { Transfer } from "@buildonspark/spark-sdk/proto/spark";
import { bytesToHex } from "@noble/hashes/utils";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import CardForm from "../../components/CardForm";
import TransactionDetailRow from "../../components/TransactionDetailRow";
import { usePaginatedTransfers } from "../../hooks/usePaginatedTransfers";
import ArrowLeft from "../../icons/ArrowLeft";
import { Routes } from "../../routes";
import { PERMANENT_CURRENCIES, useWallet } from "../../store/wallet";
import { Currency } from "../../utils/currency";

interface Transaction {
  id: string;
  value: number;
  asset: Currency;
  counterparty: string;
  transactionType: "send" | "receive";
}

export default function Transactions() {
  const { sparkAddress } = useWallet();
  const navigate = useNavigate();

  const [allTransactions, setAllTransactions] = useState<Transaction[]>([]);
  const [pageParams, setPageParams] = useState({ limit: 10, offset: 0 });
  const [hasMore, setHasMore] = useState(true);

  const transfersQuery = usePaginatedTransfers(pageParams);

  const isLoadingMore = useRef(false);
  const observerTarget = useRef<HTMLDivElement>(null);
  const scrollContainerRef = useRef<HTMLDivElement>(null);

  const processedData = useMemo(() => {
    if (!transfersQuery.data) return { transactions: [], hasMore: false };

    const transactions = transfersQuery.data.transfers.map(
      (transfer: Transfer) => {
        const txType: "send" | "receive" =
          bytesToHex(transfer.senderIdentityPublicKey) === sparkAddress
            ? "send"
            : "receive";

        const counterparty =
          txType === "send"
            ? bytesToHex(transfer.receiverIdentityPublicKey)
            : bytesToHex(transfer.senderIdentityPublicKey);

        return {
          id: transfer.id,
          value: transfer.totalValue,
          asset: PERMANENT_CURRENCIES.get("BTC")!,
          counterparty,
          transactionType: txType,
        };
      },
    );

    const hasMore = transfersQuery.data.transfers.length >= pageParams.limit;
    setHasMore(hasMore);
    return { transactions, hasMore };
  }, [transfersQuery.data, pageParams.limit, sparkAddress]);

  const loadMoreTransactions = useCallback(() => {
    if (
      !transfersQuery.isLoading &&
      processedData.hasMore &&
      !isLoadingMore.current
    ) {
      isLoadingMore.current = true;
      setPageParams((prev) => ({
        ...prev,
        offset: prev.offset + prev.limit,
      }));
    }
  }, [transfersQuery.isLoading, processedData.hasMore]);

  useEffect(() => {
    if (!transfersQuery.data || transfersQuery.isLoading) return;

    if (pageParams.offset === 0) {
      setAllTransactions(processedData.transactions);
    } else {
      setAllTransactions((prev) => [...prev, ...processedData.transactions]);
    }
    // Reset loading flag
    isLoadingMore.current = false;
  }, [
    transfersQuery.data,
    transfersQuery.isLoading,
    pageParams.offset,
    processedData.transactions,
  ]);

  useEffect(() => {
    if (!scrollContainerRef.current || !observerTarget.current) return;
    const observer = new IntersectionObserver(
      (entries) => {
        if (entries[0].isIntersecting && !transfersQuery.isLoading && hasMore) {
          console.log("Intersection observed within scrollable container");
          loadMoreTransactions();
        }
      },
      {
        root: scrollContainerRef.current,
        threshold: 0.1,
        rootMargin: "20px 0px",
      },
    );

    observer.observe(observerTarget.current);

    return () => {
      observer.disconnect();
    };
  }, [loadMoreTransactions, hasMore, transfersQuery.isLoading]);

  // Add a scroll event listener
  useEffect(() => {
    const container = scrollContainerRef.current;
    if (!container) return;

    const handleScroll = () => {
      if (transfersQuery.isLoading || !hasMore) return;
      // Check if we've scrolled near the bottom
      const scrollPosition = container.scrollTop + container.clientHeight;
      const scrollThreshold = container.scrollHeight - 30; // 30px from bottom

      if (scrollPosition >= scrollThreshold) {
        loadMoreTransactions();
      }
    };
    container.addEventListener("scroll", handleScroll);
    return () => container.removeEventListener("scroll", handleScroll);
  }, [loadMoreTransactions, hasMore, transfersQuery.isLoading]);

  return (
    <CardForm
      topTitle="All transactions"
      logoLeft={<ArrowLeft />}
      logoLeftClick={() => {
        navigate(Routes.Wallet);
      }}
      primaryButtonDisabled
    >
      <div
        className="scrollbar-hide h-full overflow-y-auto text-[13px] text-[#F9F9F999]"
        ref={scrollContainerRef}
        style={{ scrollBehavior: "smooth" }}
      >
        {transfersQuery.isLoading || allTransactions.length > 0 ? (
          <>
            {allTransactions.map((transaction) => (
              <TransactionDetailRow
                key={transaction.id}
                transactionType={transaction.transactionType}
                asset={transaction.asset}
                assetAmount={transaction.value}
                counterparty={transaction.counterparty}
              />
            ))}
            <div
              ref={observerTarget}
              className="m-2xs flex h-lg items-center justify-center"
            >
              {transfersQuery.isLoading ? (
                <span>Loading more transactions...</span>
              ) : hasMore ? (
                <span>Scroll for more</span>
              ) : (
                <span>End of transaction history</span>
              )}
            </div>
          </>
        ) : transfersQuery.isLoading ? (
          <div className="flex h-full w-full items-center justify-center text-[12px] text-[#F9F9F999]">
            Loading...
          </div>
        ) : (
          <div className="flex h-full w-full items-center justify-center">
            <span className="text-[13px] text-[#F9F9F999]">
              No transactions found
            </span>
          </div>
        )}
      </div>
    </CardForm>
  );
}
