//
//  SparkTests.swift
//  SparkTests
//
//  Created by Zhen Lu on 12/18/24.
//  Copyright © 2024 Lightspark Group, Inc. All rights reserved.
//


import Testing
import Spark
import GRPC

struct SparkTests {

    func createTestWallet() throws -> Wallet {
        let eventLoopGroup = PlatformSupport.makeEventLoopGroup(loopCount: 1)
        let channel = try GRPCChannelPool.with(target: .host("localhost", port: 8535), transportSecurity: .plaintext, eventLoopGroup: eventLoopGroup)
        let client = Spark_SparkServiceAsyncClient(channel: channel)
        let wallet = try Wallet(walletClient: client)
        return wallet
    }

    func mockDummyTx(dummyTx: DummyTx) async throws {
        let eventLoopGroup = PlatformSupport.makeEventLoopGroup(loopCount: 1)
        let channel = try GRPCChannelPool.with(target: .host("localhost", port: 8535), transportSecurity: .plaintext, eventLoopGroup: eventLoopGroup)
        let client = Mock_MockServiceAsyncClient(channel: channel)
        var request = Mock_SetMockOnchainTxRequest()
        let hexString = dummyTx.tx.map { String(format: "%02x", $0) }.joined()
        request.tx = hexString
        request.txid = dummyTx.txid
        let _ = try await client.set_mock_onchain_tx(request)
    }

    @Test func testGenerateDepositAddress() async throws {
        let wallet = try createTestWallet()
        let address = try await wallet.generateDepositAddress()
        assert(address.address.count > 0)
    }

    @Test func testCreateTree() async throws {
        let wallet = try createTestWallet()
        let address = try await wallet.generateDepositAddress()

        let dummyTx = try createDummyTx(address: address.address, amountSats: 32768)
        print(dummyTx.txid)

        try await mockDummyTx(dummyTx: dummyTx)

        let response = try await wallet.createTree(onchainTx: dummyTx.tx, onchainTxId: dummyTx.txid, vout: 0, address: address, network: "regtest")
        print(response)
    }
}
