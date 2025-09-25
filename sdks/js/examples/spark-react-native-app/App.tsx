/**
 * Sample React Native App
 * https://github.com/facebook/react-native
 *
 * @format
 */

import { IssuerSparkWallet } from '@buildonspark/issuer-sdk';
import { createDummyTx } from '@buildonspark/spark-sdk/native/spark-frost';
import { Fragment, useState } from 'react';
import {
  Button,
  SafeAreaView,
  StyleSheet,
  Text,
  useColorScheme,
  View,
  ActivityIndicator,
} from 'react-native';

function App() {
  const isDarkMode = useColorScheme() === 'dark';

  const [wallet, setWallet] = useState<IssuerSparkWallet | null>(null);
  const [invoice, setInvoice] = useState<string | null>(null);
  const [dummyTx, setDummyTx] = useState<string | null>(null);
  const [isConnecting, setIsConnecting] = useState(false);
  const [isCreatingInvoice, setIsCreatingInvoice] = useState(false);
  const [isTestingBindings, setIsTestingBindings] = useState(false);
  const [sparkAddress, setSparkAddress] = useState<string | null>(null);
  const [balance, setBalance] = useState<string | null>(null);
  const [isLoadingBalance, setIsLoadingBalance] = useState(false);
  const [isCreatingTestToken, setIsCreatingTestToken] = useState(false);
  const [testTokenTxId, setTestTokenTxId] = useState<string | null>(null);

  const connectWallet = async () => {
    try {
      setIsConnecting(true);
      setIsLoadingBalance(true);
      setInvoice(null);
      setDummyTx(null);
      setTestTokenTxId(null);
      const { wallet } = await IssuerSparkWallet.initialize({
        options: {
          network: 'REGTEST',
        },
      });
      setWallet(wallet);
      const addr = await wallet.getSparkAddress();
      const { balance: bal } = await wallet.getBalance();
      setSparkAddress(addr);
      setBalance(bal.toString());
    } catch (error) {
      console.error('Wallet connection error:', error);
    } finally {
      setIsConnecting(false);
      setIsLoadingBalance(false);
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

  const getBalance = async () => {
    try {
      setIsLoadingBalance(true);
      const balance = await wallet?.getBalance();
      setBalance(balance?.balance.toString());
    } catch (error) {
      console.error('Get balance error:', error);
    } finally {
      setIsLoadingBalance(false);
    }
  };

  const createTestToken = async () => {
    try {
      setIsCreatingTestToken(true);
      const testTokenTxId = await wallet?.createToken({
        tokenName: 'Test Token',
        tokenTicker: 'TEST',
        decimals: 0,
        isFreezable: false,
        maxSupply: 0n,
      });
      setTestTokenTxId(testTokenTxId);
    } catch (error) {
      console.error('Create test token error:', error);
    } finally {
      setIsCreatingTestToken(false);
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
          title={isLoadingBalance ? 'Loading Balance...' : 'Get Balance'}
          onPress={getBalance}
          disabled={isLoadingBalance || !wallet}
          testID="get-balance-button"
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
        <Button
          title={
            isCreatingTestToken ? 'Creating Test Token...' : 'Create Test Token'
          }
          onPress={createTestToken}
          disabled={isCreatingTestToken || !wallet}
          testID="create-test-token-button"
        />
        {wallet && (
          <Text style={styles.successText} testID="wallet-status">
            ✅ Wallet Spark Address:
          </Text>
        )}
        {wallet && sparkAddress && (
          <Text
            selectable
            style={styles.infoText}
            testID="wallet-spark-address"
          >
            {isConnecting ? 'Loading...' : sparkAddress}
          </Text>
        )}
        {wallet && balance && (
          <Fragment>
            <Text selectable style={styles.infoText} testID="wallet-balance">
              Balance: {isLoadingBalance ? 'Loading...' : `${balance} sats`}
            </Text>
          </Fragment>
        )}
        {invoice && (
          <Fragment>
            <Text style={styles.successText}>✅ Invoice Created:</Text>
            <Text selectable style={styles.infoText} testID="invoice-display">
              {invoice}
            </Text>
          </Fragment>
        )}
        {dummyTx && (
          <Fragment>
            <Text style={styles.successText}>✅ Dummy Tx Created:</Text>
            <Text selectable style={styles.infoText} testID="dummy-tx-display">
              {dummyTx}
            </Text>
          </Fragment>
        )}
        {testTokenTxId && (
          <Fragment>
            <Text style={styles.successText}>✅ Test Token Tx ID:</Text>
            <Text
              selectable
              style={styles.infoText}
              testID="test-token-tx-id-display"
            >
              {testTokenTxId}
            </Text>
          </Fragment>
        )}
      </View>
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  container: {
    margin: 24,
  },
  successText: {
    marginTop: 14,
    fontSize: 14,
    color: 'green',
  },
  infoText: {
    marginTop: 14,
    fontSize: 14,
    color: 'blue',
  },
});

export default App;
