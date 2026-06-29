const SCROLL_VIEW_ID = 'test-screen-scroll';
const OPERATION_ERROR_ID = 'operation-error';
const SCROLL_STEPS_BEFORE_REVERSING = 8;

function createTestHelpers({ timeout, longTimeout }) {
  async function sleep(ms) {
    await new Promise(resolve => setTimeout(resolve, ms));
  }

  async function isVisible(testId) {
    try {
      await expect(element(by.id(testId))).toBeVisible();
      return true;
    } catch {
      return false;
    }
  }

  async function exists(testId) {
    try {
      await expect(element(by.id(testId))).toExist();
      return true;
    } catch {
      return false;
    }
  }

  async function scrollToId(testId) {
    if (await isVisible(testId)) {
      return true;
    }

    try {
      await waitFor(element(by.id(testId)))
        .toBeVisible()
        .whileElement(by.id(SCROLL_VIEW_ID))
        .scroll(220, 'down');
      return true;
    } catch {
      // The previous test may have left the ScrollView near the bottom.
    }

    try {
      await waitFor(element(by.id(testId)))
        .toBeVisible()
        .whileElement(by.id(SCROLL_VIEW_ID))
        .scroll(220, 'up');
      return true;
    } catch {
      return false;
    }
  }

  async function tryScroll(direction) {
    try {
      await element(by.id(SCROLL_VIEW_ID)).scroll(220, direction);
    } catch {
      // The view may already be at the requested edge.
    }
  }

  async function revealIfExists(testId) {
    if (!(await exists(testId))) {
      return false;
    }
    await scrollToId(testId);
    return true;
  }

  async function tapById(testId) {
    if (!(await scrollToId(testId))) {
      await expect(element(by.id(testId))).toExist();
    }
    await element(by.id(testId)).tap();
  }

  async function getElementText(testId) {
    const attrs = await element(by.id(testId)).getAttributes();
    return attrs.text ?? JSON.stringify(attrs);
  }

  async function waitForEither(successId, errorId, waitTimeout) {
    const start = Date.now();
    let direction = 'down';
    let scrollStepsInDirection = 0;
    while (Date.now() - start < waitTimeout) {
      if (await revealIfExists(successId)) {
        return 'success';
      }
      if (await revealIfExists(errorId)) {
        return 'error';
      }
      await tryScroll(direction);
      scrollStepsInDirection += 1;
      if (scrollStepsInDirection >= SCROLL_STEPS_BEFORE_REVERSING) {
        direction = direction === 'down' ? 'up' : 'down';
        scrollStepsInDirection = 0;
      }
      await sleep(500);
    }
    throw new Error(
      `Timed out after ${waitTimeout}ms waiting for "${successId}" or "${errorId}"`,
    );
  }

  async function resetOperationResults() {
    await tapById('reset-test-results-button');
  }

  async function openTestScreen() {
    await device.disableSynchronization();
    await waitFor(element(by.id('open-test-screen-button')))
      .toBeVisible()
      .withTimeout(timeout);
    await element(by.id('open-test-screen-button')).tap();
    await waitFor(element(by.id('connect-wallet-button')))
      .toBeVisible()
      .withTimeout(timeout);
  }

  async function ensureConnectedWallet() {
    if (await exists('wallet-spark-address')) {
      await scrollToId('wallet-spark-address');
      return;
    }

    await device.disableSynchronization();
    if (!(await exists('wallet-status'))) {
      await tapById('connect-wallet-button');
    }

    const result = await waitForEither(
      'wallet-spark-address',
      'wallet-error',
      longTimeout,
    );
    if (result === 'error') {
      throw new Error(
        `Wallet connection failed: ${await getElementText('wallet-error')}`,
      );
    }

    await scrollToId('wallet-status');
    await expect(element(by.id('wallet-status'))).toBeVisible();
    await scrollToId('wallet-spark-address');
    await expect(element(by.id('wallet-spark-address'))).toBeVisible();
  }

  async function runLongOperation({
    buttonId,
    successId,
    timeout: operationTimeout = longTimeout,
    errorId = OPERATION_ERROR_ID,
  }) {
    await ensureConnectedWallet();
    await resetOperationResults();

    await device.disableSynchronization();
    await tapById(buttonId);
    const result = await waitForEither(successId, errorId, operationTimeout);
    if (result === 'error') {
      throw new Error(await getElementText(errorId));
    }

    await scrollToId(successId);
    await expect(element(by.id(successId))).toExist();
  }

  return {
    ensureConnectedWallet,
    openTestScreen,
    resetOperationResults,
    runLongOperation,
  };
}

module.exports = { createTestHelpers };
