import { bytesToNumberBE, hexToBytes } from "@noble/curves/abstract/utils";
import { SparkWallet } from "@buildonspark/spark-js-sdk";
import { IssuerTokenTransactionService } from "./token-transactions.js";
import { TokenFreezeService } from "../freeze.js";
const BURN_ADDRESS = "020202020202020202020202020202020202020202020202020202020202020202";
export class IssuerSparkWallet extends SparkWallet {
    issuerTokenTransactionService;
    tokenFreezeService;
    constructor(network, signer) {
        super(network, signer);
        this.issuerTokenTransactionService = new IssuerTokenTransactionService(this.config, this.connectionManager);
        this.tokenFreezeService = new TokenFreezeService(this.config, this.connectionManager);
    }
    async getIssuerTokenBalance() {
        const publicKey = await this.getIdentityPublicKey();
        const balance = await this.getTokenBalance(publicKey);
        const allLeaves = await this.getAllTokenLeaves();
        // Get the leaves for the issuer token public key from the map
        const issuerTokenLeaves = allLeaves.get(publicKey) || [];
        return await this.getTokenBalance(publicKey);
    }
    async mintIssuerTokens(tokenAmount) {
        var tokenPublicKey = await super.getIdentityPublicKey();
        const tokenTransaction = await this.issuerTokenTransactionService.constructMintTokenTransaction(hexToBytes(tokenPublicKey), tokenAmount);
        return await this.issuerTokenTransactionService.broadcastTokenTransaction(tokenTransaction);
    }
    async transferIssuerTokens(tokenAmount, recipientPublicKey) {
        const tokenPublicKey = await super.getIdentityPublicKey();
        return await super.transferTokens(tokenPublicKey, tokenAmount, recipientPublicKey);
    }
    async consolidateIssuerTokenLeaves() {
        const tokenPublicKey = await super.getIdentityPublicKey();
        return await super.consolidateTokenLeaves(tokenPublicKey);
    }
    async burnIssuerTokens(tokenAmount, selectedLeaves) {
        await this.transferTokens(await this.getIdentityPublicKey(), tokenAmount, BURN_ADDRESS, selectedLeaves);
    }
    async freezeIssuerTokens(ownerPublicKey) {
        await this.syncTokenLeaves();
        const tokenPublicKey = await super.getIdentityPublicKey();
        const response = await this.tokenFreezeService.freezeTokens(hexToBytes(ownerPublicKey), hexToBytes(tokenPublicKey));
        // Convert the Uint8Array to a bigint
        const tokenAmount = bytesToNumberBE(response.impactedTokenAmount);
        return {
            impactedLeafIds: response.impactedLeafIds,
            impactedTokenAmount: tokenAmount,
        };
    }
    async unfreezeIssuerTokens(ownerPublicKey) {
        await this.syncTokenLeaves();
        const tokenPublicKey = await super.getIdentityPublicKey();
        const response = await this.tokenFreezeService.unfreezeTokens(hexToBytes(ownerPublicKey), hexToBytes(tokenPublicKey));
        const tokenAmount = bytesToNumberBE(response.impactedTokenAmount);
        return {
            impactedLeafIds: response.impactedLeafIds,
            impactedTokenAmount: tokenAmount,
        };
    }
}
//# sourceMappingURL=wallet.js.map