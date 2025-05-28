package chain

import (
	"testing"

	"github.com/btcsuite/btcd/chaincfg"
	"github.com/btcsuite/btcd/txscript"
	"github.com/btcsuite/btcd/wire"
	"github.com/stretchr/testify/assert"
)

func TestProcessTransactions(t *testing.T) {
	// Create test network params
	params := &chaincfg.TestNet3Params

	tests := []struct {
		name           string
		txs            []wire.MsgTx
		expectedAddrs  int
		expectedTxids  int
		expectedError  bool
		checkAddresses func(t *testing.T, addresses []string, utxoMap map[string][]AddressDepositUtxo)
	}{
		{
			name:          "empty transactions",
			txs:           []wire.MsgTx{},
			expectedAddrs: 0,
			expectedTxids: 0,
			expectedError: false,
			checkAddresses: func(t *testing.T, addresses []string, utxoMap map[string][]AddressDepositUtxo) {
				assert.Empty(t, addresses)
				assert.Empty(t, utxoMap)
			},
		},
		{
			name: "single transaction with one output",
			txs: func() []wire.MsgTx {
				tx := wire.MsgTx{}
				// Create a simple P2PKH output script (OP_DUP OP_HASH160 <pubkeyhash> OP_EQUALVERIFY OP_CHECKSIG)
				script := []byte{
					txscript.OP_DUP,
					txscript.OP_HASH160,
					0x14, // 20 bytes
					0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09,
					0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13,
					txscript.OP_EQUALVERIFY,
					txscript.OP_CHECKSIG,
				}
				tx.AddTxOut(wire.NewTxOut(1000, script))
				return []wire.MsgTx{tx}
			}(),
			expectedAddrs: 1,
			expectedTxids: 1,
			expectedError: false,
			checkAddresses: func(t *testing.T, addresses []string, utxoMap map[string][]AddressDepositUtxo) {
				assert.Len(t, addresses, 1)
				assert.Len(t, utxoMap, 1)
				utxos, exists := utxoMap[addresses[0]]
				assert.True(t, exists)
				assert.Equal(t, uint64(1000), utxos[0].amount)
				assert.Equal(t, uint32(0), utxos[0].idx)
			},
		},
		{
			name: "multiple transactions with multiple outputs",
			txs: func() []wire.MsgTx {
				tx1 := wire.MsgTx{}
				tx2 := wire.MsgTx{}

				// Create two different P2PKH output scripts
				script1 := []byte{
					txscript.OP_DUP,
					txscript.OP_HASH160,
					0x14, // 20 bytes
					0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09,
					0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13,
					txscript.OP_EQUALVERIFY,
					txscript.OP_CHECKSIG,
				}
				script2 := []byte{
					txscript.OP_DUP,
					txscript.OP_HASH160,
					0x14, // 20 bytes
					0x13, 0x12, 0x11, 0x10, 0x0f, 0x0e, 0x0d, 0x0c, 0x0b, 0x0a,
					0x09, 0x08, 0x07, 0x06, 0x05, 0x04, 0x03, 0x02, 0x01, 0x00,
					txscript.OP_EQUALVERIFY,
					txscript.OP_CHECKSIG,
				}

				tx1.AddTxOut(wire.NewTxOut(1000, script1))
				tx1.AddTxOut(wire.NewTxOut(2000, script2))
				tx2.AddTxOut(wire.NewTxOut(3000, script1))

				return []wire.MsgTx{tx1, tx2}
			}(),
			expectedAddrs: 2, // Two unique addresses
			expectedTxids: 2, // Two transactions
			expectedError: false,
			checkAddresses: func(t *testing.T, addresses []string, utxoMap map[string][]AddressDepositUtxo) {
				assert.Len(t, addresses, 2)
				assert.Len(t, utxoMap, 2)
				foundSingleUtxoAddress := false
				foundMultipleUtxoAddress := false
				for _, utxos := range utxoMap {
					if len(utxos) == 2 {
						foundMultipleUtxoAddress = true
					} else if len(utxos) == 1 {
						foundSingleUtxoAddress = true
					}
				}
				assert.True(t, foundSingleUtxoAddress)
				assert.True(t, foundMultipleUtxoAddress)
			},
		},
		{
			name: "multiple transactions to the same address",
			txs: func() []wire.MsgTx {
				tx1 := wire.MsgTx{}
				tx2 := wire.MsgTx{}

				// Create two different P2PKH output scripts
				script1 := []byte{
					txscript.OP_DUP,
					txscript.OP_HASH160,
					0x14, // 20 bytes
					0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08, 0x09,
					0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10, 0x11, 0x12, 0x13,
					txscript.OP_EQUALVERIFY,
					txscript.OP_CHECKSIG,
				}

				tx1.AddTxOut(wire.NewTxOut(1000, script1))
				tx2.AddTxOut(wire.NewTxOut(3000, script1))

				return []wire.MsgTx{tx1, tx2}
			}(),
			expectedAddrs: 1, // One unique address
			expectedTxids: 2, // Two transactions
			expectedError: false,
			checkAddresses: func(t *testing.T, addresses []string, utxoMap map[string][]AddressDepositUtxo) {
				assert.Len(t, addresses, 1)
				assert.Len(t, utxoMap, 1)
				assert.Len(t, utxoMap[addresses[0]], 2)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			confirmedTxHashSet, creditedAddresses, addressToUtxoMap, err := processTransactions(tt.txs, params)

			if tt.expectedError {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)
			assert.Len(t, confirmedTxHashSet, tt.expectedTxids)
			tt.checkAddresses(t, creditedAddresses, addressToUtxoMap)
		})
	}
}
