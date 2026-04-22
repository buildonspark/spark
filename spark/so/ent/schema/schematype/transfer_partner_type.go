package schematype

// TransferPartnerType categorizes the partner-attributed operation.
type TransferPartnerType string

const (
	TransferPartnerTypeLightningSend    TransferPartnerType = "LIGHTNING_SEND"
	TransferPartnerTypeLightningReceive TransferPartnerType = "LIGHTNING_RECEIVE"
	TransferPartnerTypeTransfer         TransferPartnerType = "TRANSFER"
	TransferPartnerTypeCooperativeExit  TransferPartnerType = "COOPERATIVE_EXIT"
)

// Values returns the valid enum values for ent schema validation.
func (TransferPartnerType) Values() []string {
	return []string{
		string(TransferPartnerTypeLightningSend),
		string(TransferPartnerTypeLightningReceive),
		string(TransferPartnerTypeTransfer),
		string(TransferPartnerTypeCooperativeExit),
	}
}
