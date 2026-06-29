import { IssuerSparkWallet } from '@buildonspark/issuer-sdk';
import { getSparkFrost, SparkWalletEvent } from '@buildonspark/spark-sdk';
import { Buffer } from 'buffer';
import { Fragment, useEffect, useRef, useState } from 'react';
import { CONFIG } from '../config';
import {
  HERMETIC_BITCOIN_RPC,
  HERMETIC_CONFIG,
} from '../config/hermeticConfig';
import { SPARK_ENV } from '../config/sparkEnv';
import {
  Button,
  SafeAreaView,
  ScrollView,
  StyleSheet,
  Text,
  View,
} from 'react-native';

const STREAM_TEST_SENDER_MNEMONIC =
  'soldier spare tell clog armed cup future grocery achieve duck butter awkward';
const STREAM_TEST_AMOUNT_SATS = 50_000;
const STREAM_TEST_MIN_SENDER_BALANCE_SATS = 50_000;
const STREAM_TEST_BOOTSTRAP_AMOUNT_SATS = 50_000;
const LOCAL_DEPOSIT_AMOUNT_SATS = 50_000;
const LOCAL_DEPOSIT_CONFIRMATION_BLOCKS = 3;
const BALANCE_WAIT_INTERVAL_MS = 1500;
const BALANCE_WAIT_ATTEMPTS = 60;
const CLAIM_EVENT_WAIT_INTERVAL_MS = 1000;
const CLAIM_EVENT_WAIT_ATTEMPTS = 90;
const STREAM_CONNECT_WAIT_INTERVAL_MS = 500;
const STREAM_CONNECT_WAIT_ATTEMPTS = 60;

type BitcoinRpcResponse<T> = {
  result: T;
  error: { message: string } | null;
};

type LocalDepositResult = {
  address: string;
  txid: string;
  autoClaimedSats: bigint;
  settledBalance: bigint;
};

type FundRawTransactionResult = {
  hex: string;
  fee: number;
  changepos: number;
};

type SignRawTransactionWithWalletResult = {
  hex: string;
  complete: boolean;
  errors?: Array<{ error?: string }>;
};

function getErrorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}

function satsToBitcoinAmount(amountSats: number): number {
  return Number((amountSats / 100_000_000).toFixed(8));
}

function App() {
  const [wallet, setWallet] = useState<IssuerSparkWallet | null>(null);
  const [invoice, setInvoice] = useState<string | null>(null);
  const [dummyTx, setDummyTx] = useState<string | null>(null);
  const [isConnecting, setIsConnecting] = useState(false);
  const [isCreatingInvoice, setIsCreatingInvoice] = useState(false);
  const [isTestingBindings, setIsTestingBindings] = useState(false);
  const [sparkAddress, setSparkAddress] = useState<string | null>(null);
  const [balance, setBalance] = useState<string | null>(null);
  const [balanceResult, setBalanceResult] = useState<string | null>(null);
  const [isLoadingBalance, setIsLoadingBalance] = useState(false);
  const [isCreatingDepositAddress, setIsCreatingDepositAddress] =
    useState(false);
  const [isFundingLocalDeposit, setIsFundingLocalDeposit] = useState(false);
  const [depositAddress, setDepositAddress] = useState<string | null>(null);
  const [depositTxid, setDepositTxid] = useState<string | null>(null);
  const [depositAutoClaimResult, setDepositAutoClaimResult] = useState<
    string | null
  >(null);
  const [isCreatingTestToken, setIsCreatingTestToken] = useState(false);
  const [testTokenTxId, setTestTokenTxId] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [operationError, setOperationError] = useState<string | null>(null);
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
  const lastConfirmedDepositBalanceRef = useRef<bigint | null>(null);
  const walletRef = useRef<IssuerSparkWallet | null>(null);
  const primaryStreamConnectedRef = useRef(false);
  const [streamStatus, setStreamStatus] = useState<string | null>(null);

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

  const waitForCondition = async (
    isReady: () => boolean,
    description: string,
  ) => {
    for (let i = 0; i < STREAM_CONNECT_WAIT_ATTEMPTS; i++) {
      if (isReady()) {
        console.log(`${description} ready after ${i} poll attempts`);
        return;
      }
      await sleep(STREAM_CONNECT_WAIT_INTERVAL_MS);
    }

    throw new Error(`Timed out waiting for ${description}`);
  };

  const createStreamConnectionTracker = (description: string) => {
    let connected = false;

    return {
      markConnected: () => {
        console.log(`${description} stream connected`);
        connected = true;
      },
      waitForConnected: () =>
        waitForCondition(() => connected, `${description} stream connection`),
    };
  };

  const waitForPrimaryStreamConnected = () =>
    waitForCondition(
      () => primaryStreamConnectedRef.current,
      'wallet stream connection',
    );

  const waitForRecordedDepositBalanceAtLeast = async (
    getRecordedBalance: () => bigint | null,
    description: string,
    minBalance: bigint,
  ): Promise<bigint> => {
    for (let i = 0; i < CLAIM_EVENT_WAIT_ATTEMPTS; i++) {
      const recordedBalance = getRecordedBalance();
      if (recordedBalance !== null && recordedBalance >= minBalance) {
        return recordedBalance;
      }

      await sleep(CLAIM_EVENT_WAIT_INTERVAL_MS);
    }

    throw new Error(
      `Timed out waiting for ${description} with balance >= ${minBalance.toString()} sats`,
    );
  };

  const createDepositConfirmationTracker = (description: string) => {
    let confirmedBalance: bigint | null = null;

    return {
      recordDepositConfirmed: (depositId: string, updatedBalance: bigint) => {
        confirmedBalance = updatedBalance;
        console.log(
          `${description} deposit confirmed: deposit=${depositId} balance=${updatedBalance.toString()}`,
        );
      },
      waitForBalanceAtLeast: (minBalance: bigint) =>
        waitForRecordedDepositBalanceAtLeast(
          () => confirmedBalance,
          `${description} deposit confirmed event`,
          minBalance,
        ),
    };
  };

  const waitForPrimaryDepositBalanceAtLeast = (minBalance: bigint) =>
    waitForRecordedDepositBalanceAtLeast(
      () => lastConfirmedDepositBalanceRef.current,
      'primary wallet deposit confirmed event',
      minBalance,
    );

  const clearOperationResults = () => {
    setInvoice(null);
    setDummyTx(null);
    setTestTokenTxId(null);
    setTransferClaimResult(null);
    setTransferClaimError(null);
    setLastClaimedTransferId(null);
    setLastClaimedTransferBalance(null);
    setBalanceResult(null);
    setDepositAddress(null);
    setDepositTxid(null);
    setDepositAutoClaimResult(null);
    setOperationError(null);
    lastClaimedTransferIdRef.current = null;
    lastClaimedTransferBalanceRef.current = null;
    lastConfirmedDepositBalanceRef.current = null;
  };

  const bitcoinRpc = async <T,>(
    method: string,
    params: unknown[],
  ): Promise<T> => {
    const rpcCredentials = Buffer.from(
      `${HERMETIC_BITCOIN_RPC.username}:${HERMETIC_BITCOIN_RPC.password}`,
      'utf8',
    ).toString('base64');

    const response = await fetch(HERMETIC_BITCOIN_RPC.url, {
      method: 'POST',
      headers: {
        Authorization: `Basic ${rpcCredentials}`,
        'Content-Type': 'application/json',
      },
      body: JSON.stringify({
        jsonrpc: '1.0',
        id: `rn-${Date.now()}`,
        method,
        params,
      }),
    });

    if (!response.ok) {
      throw new Error(`Bitcoin RPC HTTP error: ${response.status}`);
    }

    const data = (await response.json()) as BitcoinRpcResponse<T>;
    if (data.error) {
      throw new Error(`Bitcoin RPC error: ${data.error.message}`);
    }

    return data.result;
  };

  const waitForBalanceAtLeast = async (
    targetWallet: IssuerSparkWallet,
    minBalance: bigint,
  ): Promise<bigint> => {
    for (let i = 0; i < BALANCE_WAIT_ATTEMPTS; i++) {
      const { balance: currentBalance } = await targetWallet.getBalance();
      if (
        i === 0 ||
        currentBalance >= minBalance ||
        i % 10 === 0 ||
        i === BALANCE_WAIT_ATTEMPTS - 1
      ) {
        console.log(
          `Balance poll ${i + 1}/${BALANCE_WAIT_ATTEMPTS}: current=${currentBalance.toString()} min=${minBalance.toString()}`,
        );
      }
      if (currentBalance >= minBalance) {
        return currentBalance;
      }
      await sleep(BALANCE_WAIT_INTERVAL_MS);
    }

    throw new Error(
      `Timed out waiting for balance >= ${minBalance.toString()} sats`,
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

  const fundWalletWithLocalDeposit = async (
    targetWallet: IssuerSparkWallet,
    amountSats: number,
    waitForAutoClaimReady?: () => Promise<void>,
    waitForDepositAutoClaim?: (minBalance: bigint) => Promise<bigint>,
  ): Promise<LocalDepositResult> => {
    console.log(`Preparing local deposit funding for ${amountSats} sats`);
    await waitForAutoClaimReady?.();
    const { balance: beforeBalance } = await targetWallet.getBalance();
    console.log(`Local deposit before balance: ${beforeBalance.toString()}`);
    const address = await targetWallet.getSingleUseDepositAddress();
    console.log(`Local deposit address: ${address}`);

    const rawTx = await bitcoinRpc<string>('createrawtransaction', [
      [],
      [{ [address]: satsToBitcoinAmount(amountSats) }],
    ]);
    console.log(`Local deposit raw tx created for ${amountSats} sats`);
    const fundedTx = await bitcoinRpc<FundRawTransactionResult>(
      'fundrawtransaction',
      [rawTx],
    );
    console.log(
      `Local deposit funded tx prepared: changepos=${fundedTx.changepos} fee=${fundedTx.fee}`,
    );
    // The hermetic bitcoind wallet funds with segwit inputs, so signing only
    // changes witness data and preserves the txid registered by advancedDeposit.
    const depositNodes = await targetWallet.advancedDeposit(fundedTx.hex);
    console.log(
      `Local advanced deposit registered ${depositNodes?.length ?? 0} nodes`,
    );
    const signedTx = await bitcoinRpc<SignRawTransactionWithWalletResult>(
      'signrawtransactionwithwallet',
      [fundedTx.hex],
    );
    if (!signedTx.complete) {
      throw new Error(
        `Bitcoin wallet did not fully sign deposit transaction: ${
          signedTx.errors?.map(signError => signError.error).join('; ') ??
          'unknown signing error'
        }`,
      );
    }

    const txid = await bitcoinRpc<string>('sendrawtransaction', [signedTx.hex]);
    console.log(`Local deposit funding txid: ${txid}`);
    const miningAddress = await bitcoinRpc<string>('getnewaddress', []);
    console.log(
      `Mining ${LOCAL_DEPOSIT_CONFIRMATION_BLOCKS} local deposit confirmation blocks to ${miningAddress}`,
    );
    await bitcoinRpc('generatetoaddress', [
      LOCAL_DEPOSIT_CONFIRMATION_BLOCKS,
      miningAddress,
    ]);
    console.log('Local deposit confirmation blocks mined');
    await sleep(3000);

    const minClaimedBalance = beforeBalance + BigInt(amountSats);
    const settledBalance = waitForDepositAutoClaim
      ? await waitForDepositAutoClaim(minClaimedBalance)
      : await waitForBalanceAtLeast(targetWallet, minClaimedBalance);

    return {
      address,
      txid,
      autoClaimedSats: settledBalance - beforeBalance,
      settledBalance,
    };
  };

  const connectWallet = async () => {
    try {
      setIsConnecting(true);
      setIsLoadingBalance(true);
      setError(null);
      clearOperationResults();
      primaryStreamConnectedRef.current = false;
      setStreamStatus('connecting');
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
              console.log(
                `Primary wallet transfer claimed: transfer=${transferId} balance=${updatedBalanceStr}`,
              );
            },
            [SparkWalletEvent.DepositConfirmed]: (
              depositId: string,
              updatedBalance: bigint,
            ) => {
              const updatedBalanceStr = updatedBalance.toString();
              lastConfirmedDepositBalanceRef.current = updatedBalance;
              setBalance(updatedBalanceStr);
              console.log(
                `Primary wallet deposit confirmed: deposit=${depositId} balance=${updatedBalanceStr}`,
              );
            },
            [SparkWalletEvent.StreamConnected]: () => {
              console.log('Primary wallet stream connected');
              primaryStreamConnectedRef.current = true;
              setStreamStatus('connected');
            },
            [SparkWalletEvent.StreamDisconnected]: (reason: string) => {
              console.log(`Primary wallet stream disconnected: ${reason}`);
              primaryStreamConnectedRef.current = false;
              setStreamStatus(`disconnected: ${reason}`);
            },
            [SparkWalletEvent.StreamReconnecting]: () => {
              console.log('Primary wallet stream reconnecting');
              primaryStreamConnectedRef.current = false;
              setStreamStatus('reconnecting');
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
    if (!wallet) {
      setOperationError('Wallet not connected');
      return;
    }

    try {
      setIsCreatingInvoice(true);
      setOperationError(null);
      setInvoice(null);
      console.log('Creating invoice');
      console.log('Wallet found');
      const lightningInvoice = await wallet.createLightningInvoice({
        amountSats: 1000,
      });
      setInvoice(lightningInvoice.invoice.encodedInvoice);
    } catch (err) {
      setOperationError(`Invoice creation failed: ${getErrorMessage(err)}`);
      console.error('Invoice creation error:', err);
    } finally {
      setIsCreatingInvoice(false);
    }
  };

  const testBindings = async () => {
    try {
      setIsTestingBindings(true);
      setOperationError(null);
      setDummyTx(null);
      const sparkFrost = getSparkFrost();
      const generatedDummyTx = await sparkFrost.createDummyTx(
        'bcrt1qnuyejmm2l4kavspq0jqaw0fv07lg6zv3z9z3te',
        65536n,
      );
      console.log('Tx:', generatedDummyTx.txid);
      setDummyTx(generatedDummyTx.txid);
    } catch (err) {
      setOperationError(`Bindings test failed: ${getErrorMessage(err)}`);
      console.error('Test bindings error:', err);
    } finally {
      setIsTestingBindings(false);
    }
  };

  const getBalance = async () => {
    if (!wallet) {
      setOperationError('Wallet not connected');
      return;
    }

    try {
      setIsLoadingBalance(true);
      setOperationError(null);
      setBalanceResult(null);
      const walletBalance = await wallet.getBalance();
      const balanceString = walletBalance.balance.toString();
      setBalance(balanceString);
      setBalanceResult(balanceString);
    } catch (err) {
      setOperationError(`Get balance failed: ${getErrorMessage(err)}`);
      console.error('Get balance error:', err);
    } finally {
      setIsLoadingBalance(false);
    }
  };

  const createDepositAddress = async () => {
    if (!wallet) {
      setOperationError('Wallet not connected');
      return;
    }

    try {
      setIsCreatingDepositAddress(true);
      setOperationError(null);
      setDepositAddress(null);
      const address = await wallet.getSingleUseDepositAddress();
      setDepositAddress(address);
    } catch (err) {
      setOperationError(
        `Create deposit address failed: ${getErrorMessage(err)}`,
      );
      console.error('Create deposit address error:', err);
    } finally {
      setIsCreatingDepositAddress(false);
    }
  };

  const fundLocalDeposit = async () => {
    if (!wallet) {
      setOperationError('Wallet not connected');
      return;
    }

    try {
      setIsFundingLocalDeposit(true);
      setOperationError(null);
      setDepositAddress(null);
      setDepositTxid(null);
      setDepositAutoClaimResult(null);

      const result = await fundWalletWithLocalDeposit(
        wallet,
        LOCAL_DEPOSIT_AMOUNT_SATS,
        waitForPrimaryStreamConnected,
        waitForPrimaryDepositBalanceAtLeast,
      );
      setDepositAddress(result.address);
      setDepositTxid(result.txid);
      setBalance(result.settledBalance.toString());
      setBalanceResult(result.settledBalance.toString());
      setDepositAutoClaimResult(
        `auto_claimed_sats=${result.autoClaimedSats.toString()}; balance=${result.settledBalance.toString()}; txid=${result.txid}`,
      );
    } catch (err) {
      setOperationError(`Local deposit failed: ${getErrorMessage(err)}`);
      console.error('Local deposit error:', err);
    } finally {
      setIsFundingLocalDeposit(false);
    }
  };

  const createTestToken = async () => {
    if (!wallet) {
      setOperationError('Wallet not connected');
      return;
    }

    try {
      setIsCreatingTestToken(true);
      setOperationError(null);
      setTestTokenTxId(null);
      const createdTokenTxId = await wallet.createToken({
        tokenName: 'Test Token',
        tokenTicker: 'TEST',
        decimals: 0,
        isFreezable: false,
        maxSupply: 0n,
      });
      setTestTokenTxId(createdTokenTxId);
    } catch (err) {
      setOperationError(`Create test token failed: ${getErrorMessage(err)}`);
      console.error('Create test token error:', err);
    } finally {
      setIsCreatingTestToken(false);
    }
  };

  const testTransferAndClaim = async () => {
    if (!wallet) {
      setTransferClaimError('Wallet not connected');
      return;
    }

    let senderWallet: IssuerSparkWallet | null = null;

    try {
      setIsTestingTransferClaim(true);
      setTransferClaimResult(null);
      setTransferClaimError(null);
      setOperationError(null);

      const receiverSparkAddress = await wallet.getSparkAddress();
      await waitForPrimaryStreamConnected();
      const senderOptions = SPARK_ENV.isHermeticTest
        ? { network: 'LOCAL' as const, ...HERMETIC_CONFIG }
        : CONFIG;
      const senderStreamTracker =
        createStreamConnectionTracker('sender wallet');
      const senderDepositTracker =
        createDepositConfirmationTracker('sender wallet');
      const { wallet: sender } = await IssuerSparkWallet.initialize({
        mnemonicOrSeed: STREAM_TEST_SENDER_MNEMONIC,
        options: {
          ...senderOptions,
          events: {
            [SparkWalletEvent.StreamConnected]:
              senderStreamTracker.markConnected,
            [SparkWalletEvent.DepositConfirmed]:
              senderDepositTracker.recordDepositConfirmed,
          },
        },
      });
      senderWallet = sender;

      const senderSparkAddress = await senderWallet.getSparkAddress();
      const minimumBalance = BigInt(STREAM_TEST_MIN_SENDER_BALANCE_SATS);
      let { balance: senderBalance } = await senderWallet.getBalance();

      if (senderBalance < minimumBalance) {
        if (SPARK_ENV.isHermeticTest) {
          const fundingResult = await fundWalletWithLocalDeposit(
            senderWallet,
            STREAM_TEST_BOOTSTRAP_AMOUNT_SATS,
            senderStreamTracker.waitForConnected,
            senderDepositTracker.waitForBalanceAtLeast,
          );
          senderBalance = fundingResult.settledBalance;
        } else {
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
        }
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
      console.log(`Incoming transfer sent: ${incomingTransfer.id}`);

      const claimedBalance = await waitForClaimedTransfer(incomingTransfer.id);
      setTransferClaimResult(
        `transfer_id=${incomingTransfer.id}; claimed_balance=${claimedBalance ?? 'unknown'}`,
      );
    } catch (claimError) {
      const errorMessage =
        claimError instanceof Error ? claimError.message : 'Unknown error';
      setTransferClaimError(errorMessage);
      console.error('Transfer and claim test error:', claimError);
    } finally {
      if (senderWallet) {
        await senderWallet.cleanupConnections();
      }
      setIsTestingTransferClaim(false);
    }
  };

  return (
    <SafeAreaView style={styles.container}>
      <ScrollView
        contentContainerStyle={styles.content}
        testID="test-screen-scroll"
      >
        <View>
          <Button
            title="Reset Test Results"
            onPress={clearOperationResults}
            testID="reset-test-results-button"
          />
        </View>
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
          title={
            isCreatingDepositAddress
              ? 'Creating Deposit Address...'
              : 'Create Deposit Address'
          }
          onPress={createDepositAddress}
          disabled={isCreatingDepositAddress || !wallet}
          testID="create-deposit-address-button"
        />
        <Button
          title={
            isFundingLocalDeposit
              ? 'Funding Local Deposit...'
              : 'Fund + Auto-Claim Local Deposit'
          }
          onPress={fundLocalDeposit}
          disabled={isFundingLocalDeposit || !wallet}
          testID="fund-local-deposit-button"
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
        {operationError && (
          <Text style={styles.errorText} testID="operation-error">
            {operationError}
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
        {streamStatus && (
          <Text
            selectable
            style={styles.infoText}
            testID="wallet-stream-status"
          >
            Stream: {streamStatus}
          </Text>
        )}
        {balanceResult && (
          <Text selectable style={styles.infoText} testID="balance-result">
            Balance result: {balanceResult} sats
          </Text>
        )}
        {depositAddress && (
          <Fragment>
            <Text style={styles.successText}>✅ Deposit Address:</Text>
            <Text
              selectable
              style={styles.infoText}
              testID="deposit-address-display"
            >
              {depositAddress}
            </Text>
          </Fragment>
        )}
        {depositTxid && (
          <Text
            selectable
            style={styles.infoText}
            testID="deposit-txid-display"
          >
            Deposit txid: {depositTxid}
          </Text>
        )}
        {depositAutoClaimResult && (
          <Fragment>
            <Text style={styles.successText}>
              ✅ Local Deposit Auto-Claimed:
            </Text>
            <Text
              selectable
              style={styles.infoText}
              testID="deposit-auto-claim-result-display"
            >
              {depositAutoClaimResult}
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
      </ScrollView>
    </SafeAreaView>
  );
}

const styles = StyleSheet.create({
  container: {
    flex: 1,
  },
  content: {
    padding: 24,
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
