import { ExitSpeed } from '@buildonspark/spark-sdk/types';
import { NativeStackScreenProps } from '@react-navigation/native-stack';
import { useState } from 'react';
import {
  ActivityIndicator,
  Alert,
  Button,
  Clipboard,
  Platform,
  StyleSheet,
  Text,
  TextInput,
  TouchableOpacity,
  View,
} from 'react-native';
import { KeyboardAwareScrollView } from 'react-native-keyboard-aware-scroll-view';
import { SafeAreaView } from 'react-native-safe-area-context';
import { RootStackParamList } from '../App';
import { useWallet } from '../contexts/WalletContext';

type Props = NativeStackScreenProps<RootStackParamList, 'WalletDetails'>;

type SendMode = 'spark' | 'lightning' | 'bitcoin';
type ReceiveMode = 'spark' | 'lightning' | 'bitcoin';

export default function WalletDetails({ navigation }: Props) {
  const {
    wallet,
    sparkAddress,
    balance,
    isLoadingBalance,
    getBalance,
    disconnectWallet,
  } = useWallet();

  // UI State
  const [showSend, setShowSend] = useState(false);
  const [showReceive, setShowReceive] = useState(false);
  const [sendMode, setSendMode] = useState<SendMode>('spark');
  const [receiveMode, setReceiveMode] = useState<ReceiveMode>('spark');

  // Send States
  const [sparkReceiverAddress, setSparkReceiverAddress] = useState('');
  const [sparkAmount, setSparkAmount] = useState('');
  const [lightningInvoice, setLightningInvoice] = useState('');
  const [bitcoinAddress, setBitcoinAddress] = useState('');
  const [bitcoinAmount, setBitcoinAmount] = useState('');
  const [isSending, setIsSending] = useState(false);

  // Receive States
  const [receiveAmount, setReceiveAmount] = useState('');
  const [generatedInvoice, setGeneratedInvoice] = useState('');
  const [l1Address, setL1Address] = useState('');
  const [isGenerating, setIsGenerating] = useState(false);
  const [isLoadingL1Address, setIsLoadingL1Address] = useState(false);

  const handleDisconnect = () => {
    Alert.alert('Disconnect Wallet', 'Are you sure you want to disconnect?', [
      { text: 'Cancel', style: 'cancel' },
      {
        text: 'Disconnect',
        style: 'destructive',
        onPress: () => {
          disconnectWallet();
          navigation.replace('Home');
        },
      },
    ]);
  };

  const copyToClipboard = (text: string, label: string) => {
    Clipboard.setString(text);
    Alert.alert('Copied!', `${label} copied to clipboard`);
  };

  // ***** SEND FUNCTIONS *****

  const handleSendSpark = async () => {
    if (!wallet || !sparkReceiverAddress || !sparkAmount) {
      Alert.alert('Error', 'Please fill in all fields');
      return;
    }

    const amount = parseInt(sparkAmount, 10);
    if (isNaN(amount) || amount <= 0) {
      Alert.alert('Error', 'Please enter a valid amount');
      return;
    }

    try {
      setIsSending(true);
      const transfer = await wallet.transfer({
        amountSats: amount,
        receiverSparkAddress: sparkReceiverAddress,
      });

      Alert.alert(
        'Success!',
        `Spark transfer completed!\nTX ID: ${transfer?.id}`,
      );
      await getBalance();
      setSparkReceiverAddress('');
      setSparkAmount('');
      setShowSend(false);
    } catch (error) {
      console.error('Spark transfer error:', error);
      Alert.alert(
        'Error',
        `Failed to send: ${error instanceof Error ? error.message : 'Unknown error'}`,
      );
    } finally {
      setIsSending(false);
    }
  };

  const handleSendLightning = async () => {
    if (!wallet || !lightningInvoice) {
      Alert.alert('Error', 'Please enter a lightning invoice');
      return;
    }

    try {
      setIsSending(true);
      const payment = await wallet.payLightningInvoice({
        invoice: lightningInvoice,
        maxFeeSats: 1000,
        preferSpark: true,
      });

      Alert.alert(
        'Success!',
        `Lightning payment completed!\nPayment ID: ${payment?.id}`,
      );
      await getBalance();
      setLightningInvoice('');
      setShowSend(false);
    } catch (error) {
      console.error('Lightning payment error:', error);
      Alert.alert(
        'Error',
        `Failed to pay invoice: ${error instanceof Error ? error.message : 'Unknown error'}`,
      );
    } finally {
      setIsSending(false);
    }
  };

  const handleSendBitcoin = async () => {
    if (!wallet || !bitcoinAddress || !bitcoinAmount) {
      Alert.alert('Error', 'Please fill in all fields');
      return;
    }

    const amount = parseInt(bitcoinAmount, 10);
    if (isNaN(amount) || amount <= 0) {
      Alert.alert('Error', 'Please enter a valid amount');
      return;
    }

    try {
      setIsSending(true);

      // Get fee quote first
      const feeQuote = await wallet.getWithdrawalFeeQuote({
        amountSats: amount,
        withdrawalAddress: bitcoinAddress,
      });

      if (!feeQuote) {
        throw new Error('Failed to get fee quote');
      }

      // Show fee confirmation
      Alert.alert(
        'Confirm Withdrawal',
        `Amount: ${amount} sats\nFee: ${feeQuote.l1BroadcastFeeFast.originalValue + feeQuote.userFeeFast.originalValue} sats\nTotal: ${amount + Number(feeQuote.l1BroadcastFeeFast.originalValue + feeQuote.userFeeFast.originalValue)} sats`,
        [
          {
            text: 'Cancel',
            style: 'cancel',
            onPress: () => setIsSending(false),
          },
          {
            text: 'Confirm',
            onPress: async () => {
              try {
                const withdrawal = await wallet.withdraw({
                  onchainAddress: bitcoinAddress,
                  exitSpeed: ExitSpeed.FAST,
                  feeQuote: feeQuote,
                  amountSats: amount,
                  deductFeeFromWithdrawalAmount: false,
                });

                Alert.alert(
                  'Success!',
                  `Withdrawal initiated!\nRequest ID: ${withdrawal?.id}`,
                );
                await getBalance();
                setBitcoinAddress('');
                setBitcoinAmount('');
                setShowSend(false);
              } catch (error) {
                console.error('Withdrawal error:', error);
                Alert.alert(
                  'Error',
                  `Failed to withdraw: ${error instanceof Error ? error.message : 'Unknown error'}`,
                );
              } finally {
                setIsSending(false);
              }
            },
          },
        ],
      );
    } catch (error) {
      console.error('Fee quote error:', error);
      Alert.alert(
        'Error',
        `Failed to get fee quote: ${error instanceof Error ? error.message : 'Unknown error'}`,
      );
      setIsSending(false);
    }
  };

  // ***** RECEIVE FUNCTIONS *****

  const handleReceiveLightning = async () => {
    if (!wallet || !receiveAmount) {
      Alert.alert('Error', 'Please enter an amount');
      return;
    }

    const amount = parseInt(receiveAmount, 10);
    if (isNaN(amount) || amount <= 0) {
      Alert.alert('Error', 'Please enter a valid amount');
      return;
    }

    try {
      setIsGenerating(true);
      const invoice = await wallet.createLightningInvoice({
        amountSats: amount,
        memo: 'Payment request',
      });

      setGeneratedInvoice(invoice.invoice.encodedInvoice);
    } catch (error) {
      console.error('Invoice creation error:', error);
      Alert.alert(
        'Error',
        `Failed to create invoice: ${error instanceof Error ? error.message : 'Unknown error'}`,
      );
    } finally {
      setIsGenerating(false);
    }
  };

  const handleReceiveBitcoin = async () => {
    if (!wallet) return;

    try {
      setIsLoadingL1Address(true);
      const address = await wallet.getTokenL1Address();
      setL1Address(address);
    } catch (error) {
      console.error('L1 address error:', error);
      Alert.alert(
        'Error',
        `Failed to get L1 address: ${error instanceof Error ? error.message : 'Unknown error'}`,
      );
    } finally {
      setIsLoadingL1Address(false);
    }
  };

  if (!wallet) {
    return (
      <SafeAreaView style={styles.container} edges={['top', 'bottom']}>
        <View style={styles.centerContent}>
          <Text>Please connect your wallet first</Text>
          <Button
            title="Go to Home"
            onPress={() => navigation.replace('Home')}
          />
        </View>
      </SafeAreaView>
    );
  }

  return (
    <SafeAreaView style={styles.container} edges={['bottom']}>
      <KeyboardAwareScrollView
        style={styles.scrollView}
        contentContainerStyle={styles.scrollContent}
        keyboardShouldPersistTaps="handled"
        showsVerticalScrollIndicator={true}
        enableOnAndroid={true}
        enableAutomaticScroll={true}
        extraHeight={Platform.OS === 'ios' ? 250 : 200}
        extraScrollHeight={Platform.OS === 'ios' ? 100 : 150}
        keyboardOpeningTime={0}
        enableResetScrollToCoords={false}
      >
        <View style={styles.content}>
          {/* Balance Card */}
          <View style={styles.balanceCard}>
            <Text style={styles.balanceLabel}>Total Balance</Text>
            <Text style={styles.balanceAmount}>
              {isLoadingBalance ? 'Loading...' : `${balance || '0'} sats`}
            </Text>
            <TouchableOpacity
              style={styles.refreshButton}
              onPress={getBalance}
              disabled={isLoadingBalance}
            >
              <Text style={styles.refreshButtonText}>
                {isLoadingBalance ? 'Refreshing...' : '↻ Refresh'}
              </Text>
            </TouchableOpacity>
          </View>

          {/* Action Buttons */}
          <View style={styles.actionButtons}>
            <TouchableOpacity
              style={[styles.actionButton, styles.receiveButton]}
              onPress={() => {
                setShowReceive(!showReceive);
                setShowSend(false);
                setGeneratedInvoice('');
                setL1Address('');
              }}
            >
              <Text style={styles.actionButtonText}>
                ⬇ {showReceive ? 'Cancel' : 'Receive'}
              </Text>
            </TouchableOpacity>
            <TouchableOpacity
              style={[styles.actionButton, styles.sendButton]}
              onPress={() => {
                setShowSend(!showSend);
                setShowReceive(false);
              }}
            >
              <Text style={styles.actionButtonText}>
                ⬆ {showSend ? 'Cancel' : 'Send'}
              </Text>
            </TouchableOpacity>
          </View>

          {/* SEND SECTION */}
          {showSend && (
            <View style={styles.card}>
              <Text style={styles.cardTitle}>Send</Text>

              {/* Send Mode Selector */}
              <View style={styles.modeSelector}>
                <TouchableOpacity
                  style={[
                    styles.modeButton,
                    sendMode === 'spark' && styles.modeButtonActive,
                  ]}
                  onPress={() => setSendMode('spark')}
                >
                  <Text
                    style={[
                      styles.modeButtonText,
                      sendMode === 'spark' && styles.modeButtonTextActive,
                    ]}
                  >
                    Spark
                  </Text>
                </TouchableOpacity>
                <TouchableOpacity
                  style={[
                    styles.modeButton,
                    sendMode === 'lightning' && styles.modeButtonActive,
                  ]}
                  onPress={() => setSendMode('lightning')}
                >
                  <Text
                    style={[
                      styles.modeButtonText,
                      sendMode === 'lightning' && styles.modeButtonTextActive,
                    ]}
                  >
                    Lightning
                  </Text>
                </TouchableOpacity>
                <TouchableOpacity
                  style={[
                    styles.modeButton,
                    sendMode === 'bitcoin' && styles.modeButtonActive,
                  ]}
                  onPress={() => setSendMode('bitcoin')}
                >
                  <Text
                    style={[
                      styles.modeButtonText,
                      sendMode === 'bitcoin' && styles.modeButtonTextActive,
                    ]}
                  >
                    Bitcoin
                  </Text>
                </TouchableOpacity>
              </View>

              {/* Send Spark */}
              {sendMode === 'spark' && (
                <View style={styles.formContainer}>
                  <Text style={styles.inputLabel}>Spark Address</Text>
                  <TextInput
                    style={styles.input}
                    placeholder="Enter Spark address"
                    value={sparkReceiverAddress}
                    onChangeText={setSparkReceiverAddress}
                    autoCapitalize="none"
                    autoCorrect={false}
                  />
                  <Text style={styles.inputLabel}>Amount (sats)</Text>
                  <TextInput
                    style={styles.input}
                    placeholder="Enter amount"
                    value={sparkAmount}
                    onChangeText={setSparkAmount}
                    keyboardType="numeric"
                    returnKeyType="done"
                  />
                  <TouchableOpacity
                    style={[
                      styles.submitButton,
                      (!sparkReceiverAddress || !sparkAmount || isSending) &&
                        styles.submitButtonDisabled,
                    ]}
                    onPress={handleSendSpark}
                    disabled={
                      !sparkReceiverAddress || !sparkAmount || isSending
                    }
                  >
                    {isSending ? (
                      <ActivityIndicator color="white" />
                    ) : (
                      <Text style={styles.submitButtonText}>Send Spark</Text>
                    )}
                  </TouchableOpacity>
                </View>
              )}

              {/* Send Lightning */}
              {sendMode === 'lightning' && (
                <View style={styles.formContainer}>
                  <Text style={styles.inputLabel}>Lightning Invoice</Text>
                  <TextInput
                    style={[styles.input, styles.textArea]}
                    placeholder="Paste lightning invoice (BOLT11)"
                    value={lightningInvoice}
                    onChangeText={setLightningInvoice}
                    autoCapitalize="none"
                    autoCorrect={false}
                    multiline
                    numberOfLines={3}
                  />
                  <TouchableOpacity
                    style={[
                      styles.submitButton,
                      (!lightningInvoice || isSending) &&
                        styles.submitButtonDisabled,
                    ]}
                    onPress={handleSendLightning}
                    disabled={!lightningInvoice || isSending}
                  >
                    {isSending ? (
                      <ActivityIndicator color="white" />
                    ) : (
                      <Text style={styles.submitButtonText}>Pay Invoice</Text>
                    )}
                  </TouchableOpacity>
                </View>
              )}

              {/* Send Bitcoin */}
              {sendMode === 'bitcoin' && (
                <View style={styles.formContainer}>
                  <Text style={styles.inputLabel}>Bitcoin Address (L1)</Text>
                  <TextInput
                    style={styles.input}
                    placeholder="Enter Bitcoin address"
                    value={bitcoinAddress}
                    onChangeText={setBitcoinAddress}
                    autoCapitalize="none"
                    autoCorrect={false}
                  />
                  <Text style={styles.inputLabel}>Amount (sats)</Text>
                  <TextInput
                    style={styles.input}
                    placeholder="Enter amount"
                    value={bitcoinAmount}
                    onChangeText={setBitcoinAmount}
                    keyboardType="numeric"
                    returnKeyType="done"
                  />
                  <TouchableOpacity
                    style={[
                      styles.submitButton,
                      (!bitcoinAddress || !bitcoinAmount || isSending) &&
                        styles.submitButtonDisabled,
                    ]}
                    onPress={handleSendBitcoin}
                    disabled={!bitcoinAddress || !bitcoinAmount || isSending}
                  >
                    {isSending ? (
                      <ActivityIndicator color="white" />
                    ) : (
                      <Text style={styles.submitButtonText}>
                        Withdraw to Bitcoin
                      </Text>
                    )}
                  </TouchableOpacity>
                </View>
              )}
            </View>
          )}

          {/* RECEIVE SECTION */}
          {showReceive && (
            <View style={styles.card}>
              <Text style={styles.cardTitle}>Receive</Text>

              {/* Receive Mode Selector */}
              <View style={styles.modeSelector}>
                <TouchableOpacity
                  style={[
                    styles.modeButton,
                    receiveMode === 'spark' && styles.modeButtonActive,
                  ]}
                  onPress={() => {
                    setReceiveMode('spark');
                    setGeneratedInvoice('');
                    setL1Address('');
                  }}
                >
                  <Text
                    style={[
                      styles.modeButtonText,
                      receiveMode === 'spark' && styles.modeButtonTextActive,
                    ]}
                  >
                    Spark
                  </Text>
                </TouchableOpacity>
                <TouchableOpacity
                  style={[
                    styles.modeButton,
                    receiveMode === 'lightning' && styles.modeButtonActive,
                  ]}
                  onPress={() => {
                    setReceiveMode('lightning');
                    setL1Address('');
                  }}
                >
                  <Text
                    style={[
                      styles.modeButtonText,
                      receiveMode === 'lightning' &&
                        styles.modeButtonTextActive,
                    ]}
                  >
                    Lightning
                  </Text>
                </TouchableOpacity>
                <TouchableOpacity
                  style={[
                    styles.modeButton,
                    receiveMode === 'bitcoin' && styles.modeButtonActive,
                  ]}
                  onPress={() => {
                    setReceiveMode('bitcoin');
                    setGeneratedInvoice('');
                    if (!l1Address) {
                      handleReceiveBitcoin();
                    }
                  }}
                >
                  <Text
                    style={[
                      styles.modeButtonText,
                      receiveMode === 'bitcoin' && styles.modeButtonTextActive,
                    ]}
                  >
                    Bitcoin
                  </Text>
                </TouchableOpacity>
              </View>

              {/* Receive Spark */}
              {receiveMode === 'spark' && (
                <View style={styles.formContainer}>
                  <Text style={styles.receiveLabel}>Your Spark Address</Text>
                  <View style={styles.addressDisplay}>
                    <Text style={styles.addressText} selectable>
                      {sparkAddress}
                    </Text>
                  </View>
                  <TouchableOpacity
                    style={styles.copyButton}
                    onPress={() =>
                      copyToClipboard(sparkAddress!, 'Spark address')
                    }
                  >
                    <Text style={styles.copyButtonText}>Copy Address</Text>
                  </TouchableOpacity>
                </View>
              )}

              {/* Receive Lightning */}
              {receiveMode === 'lightning' && (
                <View style={styles.formContainer}>
                  {!generatedInvoice ? (
                    <>
                      <Text style={styles.inputLabel}>Amount (sats)</Text>
                      <TextInput
                        style={styles.input}
                        placeholder="Enter amount to receive"
                        value={receiveAmount}
                        onChangeText={setReceiveAmount}
                        keyboardType="numeric"
                        returnKeyType="done"
                      />
                      <TouchableOpacity
                        style={[
                          styles.submitButton,
                          (!receiveAmount || isGenerating) &&
                            styles.submitButtonDisabled,
                        ]}
                        onPress={handleReceiveLightning}
                        disabled={!receiveAmount || isGenerating}
                      >
                        {isGenerating ? (
                          <ActivityIndicator color="white" />
                        ) : (
                          <Text style={styles.submitButtonText}>
                            Create Invoice
                          </Text>
                        )}
                      </TouchableOpacity>
                    </>
                  ) : (
                    <>
                      <Text style={styles.receiveLabel}>Lightning Invoice</Text>
                      <View style={styles.addressDisplay}>
                        <Text
                          style={[styles.addressText, styles.invoiceText]}
                          selectable
                        >
                          {generatedInvoice}
                        </Text>
                      </View>
                      <TouchableOpacity
                        style={styles.copyButton}
                        onPress={() =>
                          copyToClipboard(generatedInvoice, 'Lightning invoice')
                        }
                      >
                        <Text style={styles.copyButtonText}>Copy Invoice</Text>
                      </TouchableOpacity>
                      <TouchableOpacity
                        style={styles.newInvoiceButton}
                        onPress={() => {
                          setGeneratedInvoice('');
                          setReceiveAmount('');
                        }}
                      >
                        <Text style={styles.newInvoiceButtonText}>
                          Create New Invoice
                        </Text>
                      </TouchableOpacity>
                    </>
                  )}
                </View>
              )}

              {/* Receive Bitcoin */}
              {receiveMode === 'bitcoin' && (
                <View style={styles.formContainer}>
                  {isLoadingL1Address ? (
                    <ActivityIndicator size="large" color="#007aff" />
                  ) : l1Address ? (
                    <>
                      <Text style={styles.receiveLabel}>
                        Your Bitcoin Address (L1)
                      </Text>
                      <View style={styles.addressDisplay}>
                        <Text style={styles.addressText} selectable>
                          {l1Address}
                        </Text>
                      </View>
                      <TouchableOpacity
                        style={styles.copyButton}
                        onPress={() =>
                          copyToClipboard(l1Address, 'Bitcoin address')
                        }
                      >
                        <Text style={styles.copyButtonText}>Copy Address</Text>
                      </TouchableOpacity>
                      <Text style={styles.helperText}>
                        Send Bitcoin to this address to deposit into your Spark
                        wallet
                      </Text>
                    </>
                  ) : (
                    <TouchableOpacity
                      style={styles.submitButton}
                      onPress={handleReceiveBitcoin}
                    >
                      <Text style={styles.submitButtonText}>
                        Show L1 Address
                      </Text>
                    </TouchableOpacity>
                  )}
                </View>
              )}
            </View>
          )}

          {/* Disconnect Button */}
          <TouchableOpacity
            style={styles.disconnectButton}
            onPress={handleDisconnect}
          >
            <Text style={styles.disconnectButtonText}>Disconnect Wallet</Text>
          </TouchableOpacity>
        </View>
      </KeyboardAwareScrollView>
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: '#f5f5f5',
  },
  centerContent: {
    flex: 1,
    justifyContent: 'center',
    alignItems: 'center',
    padding: 20,
  },
  scrollView: {
    flex: 1,
  },
  scrollContent: {
    flexGrow: 1,
    paddingBottom: 100, // Increased bottom padding significantly
  },
  content: {
    padding: 20,
    paddingBottom: 60, // Extra padding at the bottom of content
  },
  balanceCard: {
    backgroundColor: '#007aff',
    borderRadius: 16,
    padding: 30,
    marginBottom: 20,
    alignItems: 'center',
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 4 },
    shadowOpacity: 0.2,
    shadowRadius: 8,
    elevation: 5,
  },
  balanceLabel: {
    fontSize: 16,
    color: 'rgba(255, 255, 255, 0.9)',
    marginBottom: 8,
  },
  balanceAmount: {
    fontSize: 36,
    fontWeight: 'bold',
    color: 'white',
    marginBottom: 15,
  },
  refreshButton: {
    paddingVertical: 8,
    paddingHorizontal: 20,
    backgroundColor: 'rgba(255, 255, 255, 0.2)',
    borderRadius: 20,
  },
  refreshButtonText: {
    color: 'white',
    fontSize: 14,
    fontWeight: '600',
  },
  actionButtons: {
    flexDirection: 'row',
    gap: 12,
    marginBottom: 20,
  },
  actionButton: {
    flex: 1,
    padding: 16,
    borderRadius: 12,
    alignItems: 'center',
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.1,
    shadowRadius: 4,
    elevation: 3,
  },
  receiveButton: {
    backgroundColor: '#34c759',
  },
  sendButton: {
    backgroundColor: '#ff9500',
  },
  actionButtonText: {
    color: 'white',
    fontSize: 16,
    fontWeight: 'bold',
  },
  card: {
    backgroundColor: 'white',
    borderRadius: 12,
    padding: 20,
    marginBottom: 20,
    shadowColor: '#000',
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.1,
    shadowRadius: 4,
    elevation: 3,
  },
  cardTitle: {
    fontSize: 20,
    fontWeight: 'bold',
    marginBottom: 15,
    color: '#333',
  },
  modeSelector: {
    flexDirection: 'row',
    marginBottom: 20,
    borderRadius: 8,
    overflow: 'hidden',
    borderWidth: 1,
    borderColor: '#007aff',
  },
  modeButton: {
    flex: 1,
    paddingVertical: 10,
    backgroundColor: 'white',
    alignItems: 'center',
  },
  modeButtonActive: {
    backgroundColor: '#007aff',
  },
  modeButtonText: {
    fontSize: 14,
    fontWeight: '600',
    color: '#007aff',
  },
  modeButtonTextActive: {
    color: 'white',
  },
  formContainer: {
    gap: 12,
    marginBottom: 20, // Extra margin to add space below forms
  },
  inputLabel: {
    fontSize: 14,
    fontWeight: '600',
    color: '#666',
  },
  input: {
    borderWidth: 1,
    borderColor: '#ddd',
    borderRadius: 8,
    padding: 12,
    fontSize: 16,
    backgroundColor: '#f9f9f9',
  },
  textArea: {
    minHeight: 80,
    textAlignVertical: 'top',
  },
  submitButton: {
    backgroundColor: '#007aff',
    padding: 16,
    borderRadius: 8,
    alignItems: 'center',
    marginTop: 10,
    marginBottom: 20, // Extra margin below button
  },
  submitButtonDisabled: {
    backgroundColor: '#ccc',
  },
  submitButtonText: {
    color: 'white',
    fontSize: 16,
    fontWeight: 'bold',
  },
  receiveLabel: {
    fontSize: 14,
    fontWeight: '600',
    color: '#666',
    marginBottom: 8,
  },
  addressDisplay: {
    backgroundColor: '#f9f9f9',
    padding: 15,
    borderRadius: 8,
    borderWidth: 1,
    borderColor: '#ddd',
  },
  addressText: {
    fontSize: 12,
    color: '#333',
    fontFamily: 'monospace',
  },
  invoiceText: {
    fontSize: 10,
  },
  copyButton: {
    backgroundColor: '#007aff',
    padding: 12,
    borderRadius: 8,
    alignItems: 'center',
    marginTop: 10,
  },
  copyButtonText: {
    color: 'white',
    fontSize: 14,
    fontWeight: '600',
  },
  newInvoiceButton: {
    backgroundColor: 'white',
    padding: 12,
    borderRadius: 8,
    alignItems: 'center',
    marginTop: 10,
    borderWidth: 1,
    borderColor: '#007aff',
  },
  newInvoiceButtonText: {
    color: '#007aff',
    fontSize: 14,
    fontWeight: '600',
  },
  helperText: {
    fontSize: 12,
    color: '#666',
    marginTop: 10,
    fontStyle: 'italic',
    textAlign: 'center',
  },
  disconnectButton: {
    backgroundColor: 'white',
    padding: 16,
    borderRadius: 8,
    alignItems: 'center',
    borderWidth: 1,
    borderColor: '#ff3b30',
    marginTop: 10,
    marginBottom: 40, // Extra margin at the very bottom
  },
  disconnectButtonText: {
    color: '#ff3b30',
    fontSize: 16,
    fontWeight: '600',
  },
});
