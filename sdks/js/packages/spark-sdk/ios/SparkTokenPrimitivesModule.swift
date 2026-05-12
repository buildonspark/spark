import Foundation
import React

@objc(SparkTokenPrimitivesModule)
class SparkTokenPrimitivesModule: NSObject, RCTBridgeModule {

    @objc
    static func moduleName() -> String! {
        return "SparkTokenPrimitivesModule"
    }

    @objc
    static func requiresMainQueueSetup() -> Bool {
        return false
    }

    private func arrayToData(_ array: [Any]) -> Data? {
        return (array as? [Int])?.map { UInt8($0) }.data
    }

    private func optionalArrayToData(_ value: Any?) -> Data? {
        guard let array = value as? [Any] else { return nil }
        return arrayToData(array)
    }

    private func dataToArray(_ data: Data) -> [Int] {
        return Array(data).map { Int($0) }
    }

    private func parseSelectedOutput(_ dict: [String: Any]) throws -> SelectedTokenOutput {
        guard let prevHashArray = dict["previousTransactionHash"] as? [Any],
              let previousTransactionHash = arrayToData(prevHashArray) else {
            throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid previousTransactionHash format"])
        }
        guard let voutInt = dict["previousTransactionVout"] as? Int else {
            throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid previousTransactionVout format"])
        }
        guard let ownerArray = dict["ownerPublicKey"] as? [Any],
              let ownerPublicKey = arrayToData(ownerArray) else {
            throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid ownerPublicKey format"])
        }
        guard let tokenIdArray = dict["tokenIdentifier"] as? [Any],
              let tokenIdentifier = arrayToData(tokenIdArray) else {
            throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid tokenIdentifier format"])
        }
        guard let tokenAmountArray = dict["tokenAmount"] as? [Any],
              let tokenAmount = arrayToData(tokenAmountArray) else {
            throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid tokenAmount format"])
        }
        return SelectedTokenOutput(
            previousTransactionHash: previousTransactionHash,
            previousTransactionVout: UInt32(voutInt),
            ownerPublicKey: ownerPublicKey,
            tokenIdentifier: tokenIdentifier,
            tokenAmount: tokenAmount
        )
    }

    private func parseReceiverOutput(_ dict: [String: Any]) throws -> ReceiverTokenOutput {
        guard let receiverSparkAddress = dict["receiverSparkAddress"] as? String else {
            throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid receiverSparkAddress format"])
        }
        let tokenIdentifier = optionalArrayToData(dict["tokenIdentifier"])
        let tokenAmount = optionalArrayToData(dict["tokenAmount"])
        return ReceiverTokenOutput(
            receiverSparkAddress: receiverSparkAddress,
            tokenIdentifier: tokenIdentifier,
            tokenAmount: tokenAmount
        )
    }

    private func parseSignatureWithIndex(_ dict: [String: Any]) throws -> SignatureWithIndexInput {
        guard let inputIndex = dict["inputIndex"] as? Int else {
            throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid inputIndex format"])
        }
        guard let publicKeyArray = dict["publicKey"] as? [Any],
              let publicKey = arrayToData(publicKeyArray) else {
            throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid publicKey format"])
        }
        guard let signatureArray = dict["signature"] as? [Any],
              let signature = arrayToData(signatureArray) else {
            throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid signature format"])
        }
        return SignatureWithIndexInput(
            inputIndex: UInt32(inputIndex),
            publicKey: publicKey,
            signature: signature
        )
    }

    @objc(constructPartialTransferTransaction:resolve:reject:)
    func rn_constructPartialTransferTransaction(_ params: [String: Any],
                                                resolve: @escaping RCTPromiseResolveBlock,
                                                reject: @escaping RCTPromiseRejectBlock) {
        do {
            guard let identityPublicKeyArray = params["identityPublicKey"] as? [Any],
                  let identityPublicKey = arrayToData(identityPublicKeyArray) else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid identityPublicKey format"])
            }

            guard let selectedOutputsArray = params["selectedOutputs"] as? [[String: Any]] else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid selectedOutputs format"])
            }
            let selectedOutputs = try selectedOutputsArray.map { try parseSelectedOutput($0) }

            guard let receiverOutputsArray = params["receiverOutputs"] as? [[String: Any]] else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid receiverOutputs format"])
            }
            let receiverOutputs = try receiverOutputsArray.map { try parseReceiverOutput($0) }

            guard let operatorKeysArray = params["operatorIdentityPublicKeys"] as? [[Any]] else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid operatorIdentityPublicKeys format"])
            }
            let operatorIdentityPublicKeys: [Data] = try operatorKeysArray.enumerated().map { (i, arr) in
                guard let data = arrayToData(arr) else {
                    throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid operator key at index \(i)"])
                }
                return data
            }

            guard let network = params["network"] as? Int else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid network format"])
            }
            guard let validityDurationSeconds = params["validityDurationSeconds"] as? Int else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid validityDurationSeconds format"])
            }
            guard let clientCreatedTimestampUnixMicros = params["clientCreatedTimestampUnixMicros"] as? Int else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid clientCreatedTimestampUnixMicros format"])
            }
            guard let withdrawBondSats = params["withdrawBondSats"] as? Int else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid withdrawBondSats format"])
            }
            guard let withdrawRelativeBlockLocktime = params["withdrawRelativeBlockLocktime"] as? Int else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid withdrawRelativeBlockLocktime format"])
            }

            let executeBeforeUnixMicros: Int64?
            if let raw = params["executeBeforeUnixMicros"] as? Int {
                executeBeforeUnixMicros = Int64(raw)
            } else {
                executeBeforeUnixMicros = nil
            }

            let request = TransferBuildRequest(
                identityPublicKey: identityPublicKey,
                selectedOutputs: selectedOutputs,
                receiverOutputs: receiverOutputs,
                operatorIdentityPublicKeys: operatorIdentityPublicKeys,
                network: UInt32(network),
                validityDurationSeconds: UInt64(validityDurationSeconds),
                clientCreatedTimestampUnixMicros: Int64(clientCreatedTimestampUnixMicros),
                withdrawBondSats: UInt64(withdrawBondSats),
                withdrawRelativeBlockLocktime: UInt64(withdrawRelativeBlockLocktime),
                executeBeforeUnixMicros: executeBeforeUnixMicros
            )

            let result = try constructPartialTransferTransaction(request: request)

            let resultDict: [String: Any] = [
                "partialTokenTransactionBytes": dataToArray(result.partialTokenTransactionBytes),
                "partialTokenTransactionHash": dataToArray(result.partialTokenTransactionHash)
            ]
            resolve(resultDict)
        } catch {
            reject("ERROR_CONSTRUCT_PARTIAL_TRANSFER_TRANSACTION", error.localizedDescription, error)
        }
    }

    @objc(hashPartialTokenTransaction:resolve:reject:)
    func rn_hashPartialTokenTransaction(_ params: [String: Any],
                                        resolve: @escaping RCTPromiseResolveBlock,
                                        reject: @escaping RCTPromiseRejectBlock) {
        do {
            guard let bytesArray = params["partialTokenTransactionBytes"] as? [Any],
                  let bytes = arrayToData(bytesArray) else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid partialTokenTransactionBytes format"])
            }
            let result = try hashPartialTokenTransaction(partialTokenTransactionBytes: bytes)
            resolve(dataToArray(result))
        } catch {
            reject("ERROR_HASH_PARTIAL_TOKEN_TRANSACTION", error.localizedDescription, error)
        }
    }

    @objc(buildBroadcastTransactionRequest:resolve:reject:)
    func rn_buildBroadcastTransactionRequest(_ params: [String: Any],
                                             resolve: @escaping RCTPromiseResolveBlock,
                                             reject: @escaping RCTPromiseRejectBlock) {
        do {
            guard let identityPublicKeyArray = params["identityPublicKey"] as? [Any],
                  let identityPublicKey = arrayToData(identityPublicKeyArray) else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid identityPublicKey format"])
            }
            guard let bytesArray = params["partialTokenTransactionBytes"] as? [Any],
                  let partialTokenTransactionBytes = arrayToData(bytesArray) else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid partialTokenTransactionBytes format"])
            }
            guard let signaturesArray = params["ownerSignatures"] as? [[String: Any]] else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid ownerSignatures format"])
            }
            let ownerSignatures = try signaturesArray.map { try parseSignatureWithIndex($0) }

            let request = BroadcastBuildRequest(
                identityPublicKey: identityPublicKey,
                partialTokenTransactionBytes: partialTokenTransactionBytes,
                ownerSignatures: ownerSignatures
            )

            let result = try buildBroadcastTransactionRequest(request: request)
            resolve(dataToArray(result))
        } catch {
            reject("ERROR_BUILD_BROADCAST_TRANSACTION_REQUEST", error.localizedDescription, error)
        }
    }

    @objc(prepareTokenInvoice:resolve:reject:)
    func rn_prepareTokenInvoice(_ params: [String: Any],
                                resolve: @escaping RCTPromiseResolveBlock,
                                reject: @escaping RCTPromiseRejectBlock) {
        do {
            guard let receiverIdentityPublicKeyArray = params["receiverIdentityPublicKey"] as? [Any],
                  let receiverIdentityPublicKey = arrayToData(receiverIdentityPublicKeyArray) else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid receiverIdentityPublicKey format"])
            }
            guard let network = params["network"] as? Int else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid network format"])
            }
            let tokenIdentifier = optionalArrayToData(params["tokenIdentifier"])
            let tokenAmount = optionalArrayToData(params["tokenAmount"])
            let memo = params["memo"] as? String
            let senderSparkAddress = params["senderSparkAddress"] as? String
            let expiryTimeUnixMillis: UInt64?
            if let raw = params["expiryTimeUnixMillis"] as? Int {
                expiryTimeUnixMillis = UInt64(raw)
            } else {
                expiryTimeUnixMillis = nil
            }
            let invoiceId = optionalArrayToData(params["invoiceId"])

            let request = PrepareTokenInvoiceRequest(
                receiverIdentityPublicKey: receiverIdentityPublicKey,
                network: UInt32(network),
                tokenIdentifier: tokenIdentifier,
                tokenAmount: tokenAmount,
                memo: memo,
                senderSparkAddress: senderSparkAddress,
                expiryTimeUnixMillis: expiryTimeUnixMillis,
                invoiceId: invoiceId
            )

            let result = try prepareTokenInvoice(request: request)

            let resultDict: [String: Any] = [
                "sparkInvoiceFieldsBytes": dataToArray(result.sparkInvoiceFieldsBytes),
                "sparkInvoiceHash": dataToArray(result.sparkInvoiceHash),
                "unsignedSparkAddress": result.unsignedSparkAddress
            ]
            resolve(resultDict)
        } catch {
            reject("ERROR_PREPARE_TOKEN_INVOICE", error.localizedDescription, error)
        }
    }

    @objc(finalizeTokenInvoice:resolve:reject:)
    func rn_finalizeTokenInvoice(_ params: [String: Any],
                                 resolve: @escaping RCTPromiseResolveBlock,
                                 reject: @escaping RCTPromiseRejectBlock) {
        do {
            guard let receiverIdentityPublicKeyArray = params["receiverIdentityPublicKey"] as? [Any],
                  let receiverIdentityPublicKey = arrayToData(receiverIdentityPublicKeyArray) else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid receiverIdentityPublicKey format"])
            }
            guard let network = params["network"] as? Int else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid network format"])
            }
            guard let fieldsBytesArray = params["sparkInvoiceFieldsBytes"] as? [Any],
                  let sparkInvoiceFieldsBytes = arrayToData(fieldsBytesArray) else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid sparkInvoiceFieldsBytes format"])
            }
            let signature = optionalArrayToData(params["signature"])

            let request = FinalizeTokenInvoiceRequest(
                receiverIdentityPublicKey: receiverIdentityPublicKey,
                network: UInt32(network),
                sparkInvoiceFieldsBytes: sparkInvoiceFieldsBytes,
                signature: signature
            )

            let result = try finalizeTokenInvoice(request: request)
            resolve(result)
        } catch {
            reject("ERROR_FINALIZE_TOKEN_INVOICE", error.localizedDescription, error)
        }
    }

    func constantsToExport() -> [AnyHashable : Any]! {
        return [:]
    }
}

private extension Array where Element == UInt8 {
    var data: Data {
        return Data(self)
    }
}
