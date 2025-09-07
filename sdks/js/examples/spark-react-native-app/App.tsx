/**
 * Sample React Native App
 * https://github.com/facebook/react-native
 *
 * @format
 */

import {
  ReactNativeSparkSigner,
  SparkWallet,
} from '@buildonspark/spark-sdk/native';
import { createDummyTx } from '@buildonspark/spark-sdk/native/spark-frost';
import { useState } from 'react';
import {
  Button,
  SafeAreaView,
  StyleSheet,
  Text,
  useColorScheme,
  View,
} from 'react-native';

function App() {
  const isDarkMode = useColorScheme() === 'dark';

  const [wallet, setWallet] = useState<SparkWallet | null>(null);
  const [invoice, setInvoice] = useState<string | null>(null);
  const [dummyTx, setDummyTx] = useState<string | null>(null);
  const [isConnecting, setIsConnecting] = useState(false);
  const [isCreatingInvoice, setIsCreatingInvoice] = useState(false);
  const [isTestingBindings, setIsTestingBindings] = useState(false);

  const connectWallet = async () => {
    try {
      setIsConnecting(true);
      const { wallet } = await SparkWallet.initialize({
        options: {
          network: 'REGTEST',
        },
        signer: new ReactNativeSparkSigner(),
      });
      setWallet(wallet);
    } catch (error) {
      console.error('Wallet connection error:', error);
    } finally {
      setIsConnecting(false);
    }
  };

  const createInvoice = async () => {
    try {
      setIsCreatingInvoice(true);
      console.log('Creating invoice');
      if (!wallet) {
        return;
      }
      console.log('Wallet found');
      const invoice = await wallet.createLightningInvoice({
        amountSats: 1000,
      });
      setInvoice(invoice.invoice.encodedInvoice);
    } catch (error) {
      console.error('Invoice creation error:', error);
    } finally {
      setIsCreatingInvoice(false);
    }
  };

  const testBindings = async () => {
    try {
      setIsTestingBindings(true);
      const dummyTx = await createDummyTx(
        'bcrt1qnuyejmm2l4kavspq0jqaw0fv07lg6zv3z9z3te',
        65536n,
      );
      console.log('Tx:', dummyTx.txid);
      setDummyTx(dummyTx.txid);
    } catch (error) {
      console.error('Test bindings error:', error);
    } finally {
      setIsTestingBindings(false);
    }
  };

  return (
    <SafeAreaView style={styles.container}>
      <View style={{ marginTop: 24 }}>
        <Button
          title={isConnecting ? 'Connecting...' : 'Connect Wallet'}
          onPress={connectWallet}
          disabled={isConnecting}
          testID="connect-wallet-button"
        />
        <Button
          title={isCreatingInvoice ? 'Creating Invoice...' : 'Create Invoice'}
          onPress={createInvoice}
          disabled={isCreatingInvoice || !wallet}
          testID="create-invoice-button"
        />
        <Button
          title={isTestingBindings ? 'Testing Bindings...' : 'Test Bindings'}
          onPress={testBindings}
          disabled={isTestingBindings}
          testID="test-bindings-button"
        />
        {wallet && (
          <Text style={styles.statusText} testID="wallet-status">
            ✅ Wallet Connected
          </Text>
        )}
        {invoice && (
          <Text
            style={styles.invoiceText}
            testID="invoice-display"
            numberOfLines={3}
          >
            Invoice: {invoice}
          </Text>
        )}
        {dummyTx && (
          <Text style={styles.statusText} testID="dummy-tx-display">
            ✅ Dummy Tx Created: {dummyTx}
          </Text>
        )}
      </View>
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  container: {
    margin: 24,
  },
  statusText: {
    marginTop: 16,
    fontSize: 16,
    color: 'green',
  },
  invoiceText: {
    marginTop: 16,
    fontSize: 12,
    color: 'blue',
  },
});

export default App;
