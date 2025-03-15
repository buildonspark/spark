// This file only contains type definitions and interfaces.
// The actual implementation (node.js or browser.js) will be chosen
// by the package.json conditional exports.

// Export the base types and interfaces
export { Lrc20ConnectionManager } from './interface.js';
export type { ILrc20ConnectionManager, Lrc20SparkClient } from './interface.js';

// Note: createConnectionManager is exported from node.js or browser.js
// based on the package.json conditional exports