package schematype

// TransferType is the type of transfer
type TransferType string

const (
	// TransferTypePreimageSwap is the type of transfer that is a preimage swap
	TransferTypePreimageSwap TransferType = "PREIMAGE_SWAP"
	// TransferTypeCooperativeExit is the type of transfer that is a cooperative exit
	TransferTypeCooperativeExit TransferType = "COOPERATIVE_EXIT"
	// TransferTypeTransfer is the type of transfer that is a normal transfer
	TransferTypeTransfer TransferType = "TRANSFER"
	// TransferTypeSwap is the type of transfer that is a swap of leaves for other leaves.
	TransferTypeSwap TransferType = "SWAP"
	// TransferTypeCounterSwap is the type of transfer that is the other side of a swap.
	TransferTypeCounterSwap TransferType = "COUNTER_SWAP"
	// TransferTypeUtxoSwap is the type of transfer that is a swap of an utxos for leaves.
	TransferTypeUtxoSwap TransferType = "UTXO_SWAP"
	// Primary side of a Swap V3 protocol, sent by the User to the SE.
	TransferTypePrimarySwapV3 TransferType = "PRIMARY_SWAP_V3"
	// Counter side of a Swap V3 protocol, sent by the SSP to the SE.
	TransferTypeCounterSwapV3 TransferType = "COUNTER_SWAP_V3"
)

// Values returns the values of the transfer type.
func (TransferType) Values() []string {
	return []string{
		string(TransferTypePreimageSwap),
		string(TransferTypeCooperativeExit),
		string(TransferTypeTransfer),
		string(TransferTypeSwap),
		string(TransferTypeCounterSwap),
		string(TransferTypeUtxoSwap),
		string(TransferTypePrimarySwapV3),
		string(TransferTypeCounterSwapV3),
	}
}
