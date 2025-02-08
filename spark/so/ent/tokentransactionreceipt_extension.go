package ent

import (
	"encoding/hex"
)

func GetReceiptMapFromList(receipts []*TokenTransactionReceipt) (map[string]*TokenTransactionReceipt, error) {
	receiptMap := make(map[string]*TokenTransactionReceipt)
	for _, r := range receipts {
		if len(r.FinalizedTokenTransactionHash) > 0 {
			key := hex.EncodeToString(r.FinalizedTokenTransactionHash)
			receiptMap[key] = r
		}
	}
	return receiptMap, nil
}
