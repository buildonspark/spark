describe('Spark React Native App', () => {
  beforeAll(async () => {
    await device.launchApp();
  });

  beforeEach(async () => {
    await device.launchApp({
      newInstance: true,
    });
  });

  it('should show all buttons on app launch', async () => {
    await expect(element(by.id('connect-wallet-button'))).toBeVisible();
    await expect(element(by.id('create-invoice-button'))).toBeVisible();
    await expect(element(by.id('test-bindings-button'))).toBeVisible();
  });

  it('should connect wallet successfully', async () => {
    // Tap the connect wallet button
    await element(by.id('connect-wallet-button')).tap();

    // Wait a moment for the wallet to initialize
    await waitFor(element(by.id('wallet-status')))
      .toBeVisible()
      .withTimeout(5000);

    // Verify the wallet is connected by checking if create invoice is enabled
    await expect(element(by.id('create-invoice-button'))).toBeVisible();
  });

  it('should create invoice after wallet connection', async () => {
    // First connect the wallet
    await element(by.id('connect-wallet-button')).tap();

    await waitFor(element(by.id('wallet-status')))
      .toBeVisible()
      .withTimeout(5000);

    // Then create an invoice
    await element(by.id('create-invoice-button')).tap();

    await waitFor(element(by.id('invoice-display')))
      .toBeVisible()
      .withTimeout(5000);

    await expect(element(by.id('invoice-display'))).toBeVisible();
  });

  it('should test bindings successfully', async () => {
    // Test the bindings functionality
    await element(by.id('test-bindings-button')).tap();

    await waitFor(element(by.id('dummy-tx-display')))
      .toBeVisible()
      .withTimeout(5000);

    await expect(element(by.id('dummy-tx-display'))).toBeVisible();
  });

  it('should create test token after wallet connection', async () => {
    await element(by.id('connect-wallet-button')).tap();

    await waitFor(element(by.id('wallet-status')))
      .toBeVisible()
      .withTimeout(5000);

    await element(by.id('create-test-token-button')).tap();

    await waitFor(element(by.id('test-token-tx-id-display')))
      .toBeVisible()
      .withTimeout(5000);

    await expect(element(by.id('test-token-tx-id-display'))).toBeVisible();
  });

  it('should handle wallet operations in sequence', async () => {
    // Test the full flow: connect wallet -> create invoice -> test bindings
    await element(by.id('connect-wallet-button')).tap();

    await waitFor(element(by.id('wallet-status')))
      .toBeVisible()
      .withTimeout(5000);

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
