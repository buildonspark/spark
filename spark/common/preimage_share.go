package common

import "github.com/lightsparkdev/spark/common/hashstructure"

// GetStorePreimageShareSigningPayload returns the signing payload for a store_preimage_share_v2 request.
func GetStorePreimageShareSigningPayload(paymentHash []byte, encryptedShares map[string][]byte, threshold uint32, invoiceString string) []byte {
	return hashstructure.NewHasher([]string{"spark", "store_preimage_share", "signing payload"}).
		AddBytes(paymentHash).
		AddMapStringToBytes(encryptedShares).
		AddUint32(threshold).
		AddString(invoiceString).
		Hash()
}
