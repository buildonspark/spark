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
}
