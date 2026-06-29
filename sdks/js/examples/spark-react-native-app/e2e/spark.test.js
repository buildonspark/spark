const { createTestHelpers } = require('./helpers');

const TIMEOUT = 30 * 1000;
const LONG_TIMEOUT = TIMEOUT * 3;
const STARTUP_TIMEOUT = TIMEOUT * 6;
const {
  ensureConnectedWallet,
  openTestScreen,
  resetOperationResults,
  runLongOperation,
} = createTestHelpers({ timeout: TIMEOUT, longTimeout: LONG_TIMEOUT });

describe('Spark React Native App', () => {
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
      .withTimeout(STARTUP_TIMEOUT);

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

  it('creates a Lightning invoice', async () => {
    await runLongOperation({
      buttonId: 'create-invoice-button',
      successId: 'invoice-display',
      timeout: LONG_TIMEOUT,
    });
  });

  it('runs native FROST bindings', async () => {
    await runLongOperation({
      buttonId: 'test-bindings-button',
      successId: 'dummy-tx-display',
      timeout: TIMEOUT,
    });
  });

  it('creates a test token', async () => {
    await runLongOperation({
      buttonId: 'create-test-token-button',
      successId: 'test-token-tx-id-display',
      timeout: LONG_TIMEOUT,
    });
  });
});
