//
//  SparkTests.swift
//  SparkTests
//
//  Created by Zhen Lu on 12/18/24.
//  Copyright © 2024 Lightspark Group, Inc. All rights reserved.
//

import secp256k1
import Testing
import Spark
import GRPC
import Foundation

struct SparkTests {

    func createTestWallet() throws -> Wallet {
        var signingOperators: [SigningOperator] = []
        for i in 0...4 {
            signingOperators.append(try SigningOperator(
                operatorId: UInt32(i),
                identifier: "000000000000000000000000000000000000000000000000000000000000000" + String(i + 1)
            ))
        }

        let wallet = try Wallet(signingOperators: signingOperators)
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

        let response = try await wallet.createTree(
            onchainTx: dummyTx.tx,
            onchainTxId: dummyTx.txid,
            vout: 0,
            address: address,
            network: "regtest"
        )
        print(response)
    }

    @Test func testKeySubstract() throws {
        let key1 = try secp256k1.Signing.PrivateKey()
        let key2 = try secp256k1.Signing.PrivateKey()
        let _ = try key1.subtract(key2)
    }
    
    @Test func testSendTransfer() async throws {
        let wallet = try createTestWallet()
        let root = try await createTestTree(wallet: wallet)
        
        let receiverIdentityPrivateKey = try secp256k1.Signing.PrivateKey()
        guard let expiryTime = Calendar.current.date(byAdding: .hour, value: 1, to: Date()) else {
            print("Failed to create expiry time")
            return
        }
        let transfer = try await wallet.sendTransfer(
            receiverIdentityPublicKey: receiverIdentityPrivateKey.publicKey.dataRepresentation,
            leafIds: [root.id],
            expiryTime: expiryTime
        )
        print(transfer)
    }
    
    private func createTestTree(wallet: Wallet) async throws -> Spark_TreeNode{
        let address = try await wallet.generateDepositAddress()
        let dummyTx = try createDummyTx(address: address.address, amountSats: 32768)
        try await mockDummyTx(dummyTx: dummyTx)
        let response = try await wallet.createTree(
            onchainTx: dummyTx.tx,
            onchainTxId: dummyTx.txid,
            vout: 0,
            address: address,
            network: "regtest"
        )
        return response.nodes.first!
    }

    @Test func testReceiveTransfer() async throws {
        let senderWallet = try createTestWallet()
        let root = try await createTestTree(wallet: senderWallet)
        
        let receiverWallet = try createTestWallet()
        guard let expiryTime = Calendar.current.date(byAdding: .hour, value: 1, to: Date()) else {
            print("Failed to create expiry time")
            return
        }
        let senderTransfer = try await senderWallet.sendTransfer(
            receiverIdentityPublicKey: receiverWallet.getIdentityPublicKey().dataRepresentation,
            leafIds: [root.id],
            expiryTime: expiryTime
        )
        
        let receiverTransfers = try await receiverWallet.queryPendingTransfers()
        #expect(receiverTransfers.count == 1)
        #expect(receiverTransfers[0].id == senderTransfer.id)
        
        try await receiverWallet.claimTransfer(receiverTransfers[0])
    }
}

