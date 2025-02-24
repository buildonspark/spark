import { SparkWallet } from "../../spark-sdk.js";
export class SparkWalletTesting extends SparkWallet {
    getSigner() {
        return this.config.signer;
    }
    async queryPendingTransfers() {
        return await this.transferService.queryPendingTransfers();
    }
    async verifyPendingTransfer(transfer) {
        return await this.transferService.verifyPendingTransfer(transfer);
    }
}
//# sourceMappingURL=spark-testing-wallet.js.map