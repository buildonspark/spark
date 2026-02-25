const TIMEOUT = 60 * 1000;

async function waitForEither(successId, errorId, timeout) {
  const start = Date.now();
  while (Date.now() - start < timeout) {
    try {
      await expect(element(by.id(successId))).toBeVisible();
      return 'success';
    } catch (_) {
      // not visible yet
    }
    try {
      await expect(element(by.id(errorId))).toBeVisible();
      return 'error';
    } catch (_) {
      // not visible yet
    }
    await new Promise(r => setTimeout(r, 1000));
  }
  throw new Error(
    `Timed out after ${timeout}ms waiting for "${successId}" or "${errorId}"`,
  );
}

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
      .withTimeout(TIMEOUT * 3);

    // Re-enable synchronization once the app is stable
    await device.enableSynchronization();
  });

  afterAll(async () => {
    await device.terminateApp();
  });

  it('should handle wallet operations against minikube', async () => {
    // Navigate to test screen
    await waitFor(element(by.id('open-test-screen-button')))
      .toBeVisible()
      .withTimeout(TIMEOUT);

    await expect(element(by.id('open-test-screen-button'))).toBeVisible();
    await element(by.id('open-test-screen-button')).tap();

    await waitFor(element(by.id('connect-wallet-button')))
      .toBeVisible()
      .withTimeout(TIMEOUT);

    await expect(element(by.id('connect-wallet-button'))).toBeVisible();
    await expect(element(by.id('test-bindings-button'))).toBeVisible();

    // Disable synchronization during wallet connect - the SDK makes many
    // sequential network calls that can confuse Detox's idle tracking.
    await device.disableSynchronization();

    // Connect wallet to minikube operators
    await element(by.id('connect-wallet-button')).tap();

    // Wait for either success or error - fail fast on errors instead of
    // waiting the full timeout blindly.
    const result = await waitForEither(
      'wallet-status',
      'wallet-error',
      TIMEOUT * 2,
    );
    if (result === 'error') {
      const errorElement = element(by.id('wallet-error'));
      const attrs = await errorElement.getAttributes();
      throw new Error(`Wallet connection failed: ${attrs.text}`);
    }

    await device.enableSynchronization();

    await expect(element(by.id('wallet-status'))).toBeVisible();

    // Get balance
    await expect(element(by.id('get-balance-button'))).toBeVisible();
    await element(by.id('get-balance-button')).tap();

    await waitFor(element(by.id('wallet-balance')))
      .toBeVisible()
      .withTimeout(TIMEOUT);

    await expect(element(by.id('wallet-balance'))).toBeVisible();

    // Test FROST bindings (local crypto, no network)
    await element(by.id('test-bindings-button')).tap();

    await waitFor(element(by.id('dummy-tx-display')))
      .toBeVisible()
      .withTimeout(TIMEOUT);

    await expect(element(by.id('dummy-tx-display'))).toBeVisible();

    // Create test token (goes to operators, not SSP)
    await element(by.id('create-test-token-button')).tap();

    await waitFor(element(by.id('test-token-tx-id-display')))
      .toBeVisible()
      .withTimeout(TIMEOUT * 2);

    await expect(element(by.id('test-token-tx-id-display'))).toBeVisible();

    // NOTE: createInvoice is skipped - it requires SSP which is not in minikube
  });
});
