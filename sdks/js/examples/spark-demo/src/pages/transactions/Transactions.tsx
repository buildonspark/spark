import { bytesToHex } from "@noble/hashes/utils";
import { useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import CardForm from "../../components/CardForm";
import TransactionDetailRow from "../../components/TransactionDetailRow";
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
  const navigate = useNavigate();
  const { getAllTransfers, getMasterPublicKey } = useWallet();
  const [transactions, setTransactions] = useState<Transaction[]>([]);
  const [myPubKey, setMyPubKey] = useState<string>("");
  const [lastOffset, setLastOffset] = useState<number>(0);

  useEffect(() => {
    const fetchPubKey = async () => {
      const key = await getMasterPublicKey();
      setMyPubKey(key);
    };

    fetchPubKey();
  }, [getMasterPublicKey]);

  useEffect(() => {
    getAllTransfers(10, lastOffset).then((transfers) => {
      setTransactions([
        ...transfers.transfers.map((transfer, index) => {
          const txType: "send" | "receive" =
            bytesToHex(transfer.senderIdentityPublicKey) === myPubKey
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
        }),
      ]);
      setLastOffset(transfers.offset);
    });
  }, [getAllTransfers, myPubKey]);
  return (
    <CardForm
      topTitle="All transactions"
      logoLeft={<ArrowLeft />}
      logoLeftClick={() => {
        navigate(Routes.Wallet);
      }}
      primaryButtonDisabled
    >
      {transactions.map((transaction) => (
        <TransactionDetailRow
          key={transaction.id}
          transactionType={transaction.transactionType}
          asset={transaction.asset}
          assetAmount={transaction.value}
          counterparty={transaction.counterparty}
        />
      ))}
    </CardForm>
  );
}
