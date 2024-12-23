//
//  Data+Hex.swift
//  Spark
//
//  Created by Zhen Lu on 12/21/24.
//  Copyright © 2024 Lightspark Group, Inc. All rights reserved.
//

import Foundation

extension Data {
    func hexString() -> String {
        map { String(format: "%02x", $0) }.joined()
    }

    func padTo32Bytes() -> Data {
        if self.count >= 32 {
            // If data is longer than 32 bytes, truncate it
            return self
        } else {
            // If data is shorter, pad with zeros
            var paddedData = self
            let paddingNeeded = 32 - self.count
            paddedData.append(contentsOf: [UInt8](repeating: 0, count: paddingNeeded))
            return paddedData
        }
    }
}
