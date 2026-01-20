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
      .withTimeout(180000);

    // Re-enable synchronization once the app is stable
    await device.enableSynchronization();
  });

  afterAll(async () => {
    await device.terminateApp();
  });

  it('should handle wallet operations in sequence', async () => {
    await waitFor(element(by.id('open-test-screen-button')))
      .toBeVisible()
      .withTimeout(10000);

    await expect(element(by.id('open-test-screen-button'))).toBeVisible();

    await element(by.id('open-test-screen-button')).tap();

    await waitFor(element(by.id('connect-wallet-button')))
      .toBeVisible()
      .withTimeout(5000);

    await expect(element(by.id('connect-wallet-button'))).toBeVisible();
    await expect(element(by.id('create-invoice-button'))).toBeVisible();
    await expect(element(by.id('test-bindings-button'))).toBeVisible();

    await element(by.id('connect-wallet-button')).tap();

    await waitFor(element(by.id('wallet-status')))
      .toBeVisible()
      .withTimeout(5000);

    await expect(element(by.id('wallet-status'))).toBeVisible();

    await expect(element(by.id('get-balance-button'))).toBeVisible();

    await element(by.id('get-balance-button')).tap();

    await waitFor(element(by.id('wallet-balance')))
      .toBeVisible()
      .withTimeout(5000);

    await expect(element(by.id('wallet-balance'))).toBeVisible();

    await element(by.id('create-invoice-button')).tap();

    await waitFor(element(by.id('invoice-display')))
      .toBeVisible()
      .withTimeout(5000);

    await expect(element(by.id('invoice-display'))).toBeVisible();

    await element(by.id('test-bindings-button')).tap();

    await waitFor(element(by.id('dummy-tx-display')))
      .toBeVisible()
      .withTimeout(5000);

    await expect(element(by.id('dummy-tx-display'))).toBeVisible();

    await element(by.id('create-test-token-button')).tap();

    await waitFor(element(by.id('test-token-tx-id-display')))
      .toBeVisible()
      .withTimeout(5000);

    await expect(element(by.id('test-token-tx-id-display'))).toBeVisible();
  });
});
