const { createTestHelpers } = require('./helpers');

const TIMEOUT = 60 * 1000;
const LONG_TIMEOUT = TIMEOUT * 3;
const TRANSFER_CLAIM_TIMEOUT = TIMEOUT * 5;
const itAndroid = device.getPlatform() === 'android' ? it : it.skip;
const {
  ensureConnectedWallet,
  openTestScreen,
  resetOperationResults,
  runLongOperation,
} = createTestHelpers({ timeout: TIMEOUT, longTimeout: LONG_TIMEOUT });

describe('Spark React Native App (Hermetic)', () => {
  beforeAll(async () => {
    await device.installApp();

    await device.launchApp({
      newInstance: false,
      launchArgs: {
        detoxEnableSynchronization: 0,
        detoxPrintBusyIdleResources: 'YES',
      },
    });

    await waitFor(element(by.id('open-test-screen-button')))
      .toBeVisible()
      .withTimeout(LONG_TIMEOUT);

    await openTestScreen();
  });

  afterAll(async () => {
    await device.terminateApp();
  });

  it(
    'connects a wallet',
    async () => {
      await resetOperationResults();
      await ensureConnectedWallet();
    },
    LONG_TIMEOUT + TIMEOUT,
  );

  it('gets the wallet balance', async () => {
    await runLongOperation({
      buttonId: 'get-balance-button',
      successId: 'balance-result',
      timeout: TIMEOUT,
    });
  });

  it('creates a single-use deposit address', async () => {
    await runLongOperation({
      buttonId: 'create-deposit-address-button',
      successId: 'deposit-address-display',
      timeout: TIMEOUT,
    });
  });

  // Android-only for now: this local funding path relies on Android CI's
  // adb reverse bridge to the hermetic bitcoind RPC port.
  itAndroid(
    ':android: auto-claims a local deposit',
    async () => {
      await runLongOperation({
        buttonId: 'fund-local-deposit-button',
        successId: 'deposit-auto-claim-result-display',
        timeout: LONG_TIMEOUT,
      });
    },
    LONG_TIMEOUT + TIMEOUT,
  );

  it('skips Lightning invoice creation because hermetic CI lacks SSP', async () => {
    console.log(
      'Skipping Lightning invoice creation: the Spark-only hermetic CI profile does not deploy SSP.',
    );
  });

  // Android-only for now: this transfer setup relies on Android CI's
  // adb reverse bridge to the hermetic bitcoind RPC port.
  itAndroid(
    ':android: auto-claims an incoming Spark transfer',
    async () => {
      await runLongOperation({
        buttonId: 'test-transfer-claim-button',
        successId: 'transfer-claim-result-display',
        errorId: 'transfer-claim-error-display',
        timeout: TRANSFER_CLAIM_TIMEOUT,
      });
    },
    TRANSFER_CLAIM_TIMEOUT + TIMEOUT,
  );

  it('runs native FROST bindings', async () => {
    await runLongOperation({
      buttonId: 'test-bindings-button',
      successId: 'dummy-tx-display',
      timeout: TIMEOUT,
    });
  });

  it(
    'creates a test token',
    async () => {
      await runLongOperation({
        buttonId: 'create-test-token-button',
        successId: 'test-token-tx-id-display',
        timeout: LONG_TIMEOUT,
      });
    },
    LONG_TIMEOUT + TIMEOUT,
  );
});
