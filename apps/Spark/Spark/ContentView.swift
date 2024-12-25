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
            var signingOperators: [SigningOperator] = []
            for i in 0...4 {
                signingOperators.append(
                    try! SigningOperator(
                        operatorId: UInt32(i),
                        identifier: "000000000000000000000000000000000000000000000000000000000000000" + String(i + 1)
                    )
                )
            }
            let wallet = try! Wallet(signingOperators: signingOperators)
            let address = try! await wallet.generateDepositAddress()
            self.text = address.address
        }
    }
}

#Preview{
    ContentView()
}
