import crypto from "crypto";
export const getCrypto = () => {
    // Browser environment
    if (typeof window !== "undefined" && window.crypto) {
        return window.crypto;
    }
    // Node.js environment
    if (typeof global !== "undefined" && global.crypto) {
        return global.crypto;
    }
    // Node.js environment without global.crypto (older versions)
    return crypto;
};
//# sourceMappingURL=crypto.js.map