import { MockServiceClient } from "../proto/mock.js";
import { SparkServiceClient } from "../proto/spark.js";
import { WalletConfigService } from "./config.js";
export declare class ConnectionManager {
    private config;
    constructor(config: WalletConfigService);
    static createMockClient(address: string): MockServiceClient & {
        close: () => void;
    };
    private static createChannelWithTLS;
    createSparkClient(address: string, certPath?: string): Promise<SparkServiceClient & {
        close?: () => void;
    }>;
    private authenticate;
    private createSparkAuthnGrpcConnection;
    private createMiddleWare;
    private createNodeMiddleWare;
    private createBrowserMiddleWare;
    private createGrpcClient;
}
