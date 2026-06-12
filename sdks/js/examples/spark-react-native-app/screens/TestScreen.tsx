import { IssuerSparkWallet } from '@buildonspark/issuer-sdk';
import { getSparkFrost, SparkWalletEvent } from '@buildonspark/spark-sdk';
import { Fragment, useEffect, useRef, useState } from 'react';
import { CONFIG } from '../config';
import { HERMETIC_CONFIG } from '../config/hermeticConfig';
import { SPARK_ENV } from '../config/sparkEnv';
import { Button, SafeAreaView, StyleSheet, Text, View } from 'react-native';

const STREAM_TEST_SENDER_MNEMONIC =
  'soldier spare tell clog armed cup future grocery achieve duck butter awkward';
const STREAM_TEST_AMOUNT_SATS = 100;
const STREAM_TEST_MIN_SENDER_BALANCE_SATS = 300;
const STREAM_TEST_BOOTSTRAP_AMOUNT_SATS = 500;
const BALANCE_WAIT_INTERVAL_MS = 1500;
const BALANCE_WAIT_ATTEMPTS = 24;
const CLAIM_EVENT_WAIT_INTERVAL_MS = 1000;
const CLAIM_EVENT_WAIT_ATTEMPTS = 90;

function App() {
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
  const [error, setError] = useState<string | null>(null);
  const [isTestingTransferClaim, setIsTestingTransferClaim] = useState(false);
  const [lastClaimedTransferId, setLastClaimedTransferId] = useState<
    string | null
  >(null);
  const [lastClaimedTransferBalance, setLastClaimedTransferBalance] = useState<
    string | null
  >(null);
  const [transferClaimResult, setTransferClaimResult] = useState<string | null>(
    null,
  );
  const [transferClaimError, setTransferClaimError] = useState<string | null>(
    null,
  );
  const lastClaimedTransferIdRef = useRef<string | null>(null);
  const lastClaimedTransferBalanceRef = useRef<string | null>(null);
  const walletRef = useRef<IssuerSparkWallet | null>(null);

  useEffect(() => {
    walletRef.current = wallet;
  }, [wallet]);

  useEffect(() => {
    return () => {
      const currentWallet = walletRef.current;
      walletRef.current = null;
      if (currentWallet) {
        currentWallet.cleanupConnections().catch((cleanupError: unknown) => {
          console.error('Wallet cleanup error:', cleanupError);
        });
      }
    };
  }, []);

  const sleep = (ms: number) =>
    new Promise<void>(resolve => setTimeout(resolve, ms));

  const waitForBalanceAtLeast = async (
    targetWallet: IssuerSparkWallet,
    minBalance: bigint,
  ): Promise<bigint> => {
    for (let i = 0; i < BALANCE_WAIT_ATTEMPTS; i++) {
      const { balance } = await targetWallet.getBalance();
      if (balance >= minBalance) {
        return balance;
      }
      await sleep(BALANCE_WAIT_INTERVAL_MS);
    }

    throw new Error(
      `Timed out waiting for sender balance >= ${minBalance.toString()} sats`,
    );
  };

  const waitForClaimedTransfer = async (expectedTransferId: string) => {
    for (let i = 0; i < CLAIM_EVENT_WAIT_ATTEMPTS; i++) {
      if (lastClaimedTransferIdRef.current === expectedTransferId) {
        return lastClaimedTransferBalanceRef.current;
      }
      await sleep(CLAIM_EVENT_WAIT_INTERVAL_MS);
    }

    throw new Error(
      `Timed out waiting for TransferClaimed event for transfer ${expectedTransferId}`,
    );
  };

  const connectWallet = async () => {
    try {
      setIsConnecting(true);
      setIsLoadingBalance(true);
      setError(null);
      setInvoice(null);
      setDummyTx(null);
      setTestTokenTxId(null);
      setTransferClaimResult(null);
      setTransferClaimError(null);
      setLastClaimedTransferId(null);
      setLastClaimedTransferBalance(null);
      lastClaimedTransferIdRef.current = null;
      lastClaimedTransferBalanceRef.current = null;
      if (wallet) {
        await wallet.cleanupConnections();
        setWallet(null);
        setSparkAddress(null);
        setBalance(null);
      }
      const baseConfig = SPARK_ENV.isHermeticTest
        ? { network: 'LOCAL' as const, ...HERMETIC_CONFIG }
        : CONFIG;
      const { wallet: initializedWallet } = await IssuerSparkWallet.initialize({
        options: {
          ...baseConfig,
          events: {
            [SparkWalletEvent.TransferClaimed]: (
              transferId: string,
              updatedBalance: bigint,
            ) => {
              const updatedBalanceStr = updatedBalance.toString();
              setBalance(updatedBalanceStr);
              setLastClaimedTransferId(transferId);
              setLastClaimedTransferBalance(updatedBalanceStr);
              lastClaimedTransferIdRef.current = transferId;
              lastClaimedTransferBalanceRef.current = updatedBalanceStr;
            },
          },
        },
      });
      setWallet(initializedWallet);
      const addr = await initializedWallet.getSparkAddress();
      const { balance: bal } = await initializedWallet.getBalance();
      setSparkAddress(addr);
      setBalance(bal.toString());
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      console.error('Wallet connection error:', message);
      setError(message);
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
      const lightningInvoice = await wallet.createLightningInvoice({
        amountSats: 1000,
      });
      setInvoice(lightningInvoice.invoice.encodedInvoice);
    } catch (err) {
      console.error('Invoice creation error:', err);
    } finally {
      setIsCreatingInvoice(false);
    }
  };

  const testBindings = async () => {
    try {
      setIsTestingBindings(true);
      const sparkFrost = getSparkFrost();
      const generatedDummyTx = await sparkFrost.createDummyTx(
        'bcrt1qnuyejmm2l4kavspq0jqaw0fv07lg6zv3z9z3te',
        65536n,
      );
      console.log('Tx:', generatedDummyTx.txid);
      setDummyTx(generatedDummyTx.txid);
    } catch (err) {
      console.error('Test bindings error:', err);
    } finally {
      setIsTestingBindings(false);
    }
  };

  const getBalance = async () => {
    try {
      setIsLoadingBalance(true);
      const walletBalance = await wallet?.getBalance();
      setBalance(walletBalance?.balance.toString() ?? null);
    } catch (err) {
      console.error('Get balance error:', err);
    } finally {
      setIsLoadingBalance(false);
    }
  };

  const createTestToken = async () => {
    try {
      setIsCreatingTestToken(true);
      const createdTokenTxId = await wallet?.createToken({
        tokenName: 'Test Token',
        tokenTicker: 'TEST',
        decimals: 0,
        isFreezable: false,
        maxSupply: 0n,
      });
      setTestTokenTxId(createdTokenTxId ?? null);
    } catch (err) {
      console.error('Create test token error:', err);
    } finally {
      setIsCreatingTestToken(false);
    }
  };

  const testTransferAndClaim = async () => {
    if (!wallet) {
      return;
    }

    let senderWallet: IssuerSparkWallet | null = null;

    try {
      setIsTestingTransferClaim(true);
      setTransferClaimResult(null);
      setTransferClaimError(null);

      const receiverSparkAddress = await wallet.getSparkAddress();
      const senderOptions = SPARK_ENV.isHermeticTest
        ? { network: 'LOCAL' as const, ...HERMETIC_CONFIG }
        : CONFIG;
      const { wallet: sender } = await IssuerSparkWallet.initialize({
        mnemonicOrSeed: STREAM_TEST_SENDER_MNEMONIC,
        options: senderOptions,
      });
      senderWallet = sender;

      const senderSparkAddress = await senderWallet.getSparkAddress();
      const minimumBalance = BigInt(STREAM_TEST_MIN_SENDER_BALANCE_SATS);
      let { balance: senderBalance } = await senderWallet.getBalance();

      if (senderBalance < minimumBalance) {
        const { balance: receiverBalance } = await wallet.getBalance();
        const bootstrapAmount = BigInt(STREAM_TEST_BOOTSTRAP_AMOUNT_SATS);

        if (receiverBalance < bootstrapAmount) {
          throw new Error(
            `Insufficient funds for stream claim test (sender=${senderBalance.toString()} sats, receiver=${receiverBalance.toString()} sats)`,
          );
        }

        await wallet.transfer({
          amountSats: STREAM_TEST_BOOTSTRAP_AMOUNT_SATS,
          receiverSparkAddress: senderSparkAddress,
        });

        senderBalance = await waitForBalanceAtLeast(
          senderWallet,
          minimumBalance,
        );
        console.log(
          `Bootstrapped sender wallet. Sender balance: ${senderBalance.toString()} sats`,
        );
      }

      setLastClaimedTransferId(null);
      setLastClaimedTransferBalance(null);
      lastClaimedTransferIdRef.current = null;
      lastClaimedTransferBalanceRef.current = null;

      const incomingTransfer = await senderWallet.transfer({
        amountSats: STREAM_TEST_AMOUNT_SATS,
        receiverSparkAddress,
      });

      const claimedBalance = await waitForClaimedTransfer(incomingTransfer.id);
      setTransferClaimResult(
        `transfer_id=${incomingTransfer.id}; claimed_balance=${claimedBalance ?? 'unknown'}`,
      );
    } catch (error) {
      const errorMessage =
        error instanceof Error ? error.message : 'Unknown error';
      setTransferClaimError(errorMessage);
      console.error('Transfer and claim test error:', error);
    } finally {
      if (senderWallet) {
        await senderWallet.cleanupConnections();
      }
      setIsTestingTransferClaim(false);
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
        <Button
          title={
            isTestingTransferClaim
              ? 'Testing Transfer + Claim...'
              : 'Test Transfer + Claim Stream'
          }
          onPress={testTransferAndClaim}
          disabled={isTestingTransferClaim || !wallet}
          testID="test-transfer-claim-button"
        />
        {error && (
          <Text style={styles.errorText} testID="wallet-error">
            {error}
          </Text>
        )}
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
        {lastClaimedTransferId && (
          <Fragment>
            <Text style={styles.successText}>✅ Last Claimed Transfer ID:</Text>
            <Text
              selectable
              style={styles.infoText}
              testID="last-claimed-transfer-id-display"
            >
              {lastClaimedTransferId}
            </Text>
          </Fragment>
        )}
        {lastClaimedTransferBalance && (
          <Text
            selectable
            style={styles.infoText}
            testID="last-claimed-transfer-balance-display"
          >
            Claimed balance: {lastClaimedTransferBalance} sats
          </Text>
        )}
        {transferClaimResult && (
          <Fragment>
            <Text style={styles.successText}>
              ✅ Transfer + Stream Claim Verified:
            </Text>
            <Text
              selectable
              style={styles.infoText}
              testID="transfer-claim-result-display"
            >
              {transferClaimResult}
            </Text>
          </Fragment>
        )}
        {transferClaimError && (
          <Text style={styles.errorText} testID="transfer-claim-error-display">
            Transfer/claim test failed: {transferClaimError}
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
  errorText: {
    marginTop: 14,
    fontSize: 14,
    color: 'red',
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
