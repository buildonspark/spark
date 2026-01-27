package tokens

// kind is a 2-byte identifier for different BTKN protocol message types.
type kind [2]byte

// Descriptor describes a BTKN protocol message format.
type Descriptor struct {
	Prefix string
	Kind   kind
}

const (
	prefixBTKN = "BTKN"
)

var (
	kindWithdrawal = kind{0, 4}

	// btknWithdrawal describes the BTKN withdrawal announcement format.
	btknWithdrawal = Descriptor{Prefix: prefixBTKN, Kind: kindWithdrawal}
)

// Withdrawal format constants
const (
	withdrawalKindSizeBytes        = 2
	seEntityPubKeySizeBytes        = 33
	ownerSignatureSizeBytes        = 64
	withdrawalOutputVoutSizeBytes  = 2
	withdrawalSparkTxHashSizeBytes = 32
	withdrawalSparkTxVoutSizeBytes = 4

	// WithdrawalExpectedFormat describes the expected format of a BTKN withdrawal announcement.
	WithdrawalExpectedFormat = "[se_entity_pubkey(33)] + [owner_signature(64)] + [withdrawn_ttxo_count(1)] + [[vout(2)] + [spark_tx_hash(32)] + [spark_tx_vout(4)]](variable)"
)
