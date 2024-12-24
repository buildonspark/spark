//
//  ContentView.swift
//  Spark
//
//  Created by Zhen Lu on 12/18/24.
//  Copyright © 2024 Lightspark Group, Inc. All rights reserved.
//

import GRPC
import SwiftUI

struct ContentView: View {
    @State var text = ""

    var body: some View {
        VStack {
            Text(self.text)
        }
        .padding()
        .task {
            let eventLoopGroup = PlatformSupport.makeEventLoopGroup(loopCount: 1)
            let channel = try! GRPCChannelPool.with(
                target: .host("localhost", port: 8535),
                transportSecurity: .plaintext,
                eventLoopGroup: eventLoopGroup
            )
            let client = Spark_SparkServiceAsyncClient(channel: channel)
            let wallet = try! Wallet(walletClient: client)
            let address = try! await wallet.generateDepositAddress()
            self.text = address.address
        }
    }
}

#Preview{
    ContentView()
}
