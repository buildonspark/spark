import Foundation
import React

@objc(SparkFrostModule)
class SparkFrostModule: NSObject, RCTBridgeModule {
    
    @objc
    static func moduleName() -> String! {
        return "SparkFrostModule"
    }
    
    @objc
    static func requiresMainQueueSetup() -> Bool {
        return false
    }
    
    private func arrayToData(_ array: [Any]) -> Data? {
        return (array as? [Int])?.map { UInt8($0) }.data
    }
    
    private func dataToArray(_ data: Data) -> [Int] {
        return Array(data).map { Int($0) }
    }
    
    @objc(signFrost:resolve:reject:)
    func rn_SignFrost(_ params: [String: Any],
                   resolve: @escaping RCTPromiseResolveBlock,
                   reject: @escaping RCTPromiseRejectBlock) {
        do {
            guard let msgArray = params["msg"] as? [Any],
                  let msg = arrayToData(msgArray) else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid msg format"])
            }
            
            guard let keyPackageDict = params["keyPackage"] as? [String: Any],
                  let secretKeyArray = keyPackageDict["secretKey"] as? [Any],
                  let publicKeyArray = keyPackageDict["publicKey"] as? [Any],
                  let verifyingKeyArray = keyPackageDict["verifyingKey"] as? [Any],
                  let secretKey = arrayToData(secretKeyArray),
                  let publicKey = arrayToData(publicKeyArray),
                  let verifyingKey = arrayToData(verifyingKeyArray) else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid keyPackage format"])
            }
            
            let keyPackage = KeyPackage(
                secretKey: secretKey,
                publicKey: publicKey,
                verifyingKey: verifyingKey
            )
            
            guard let nonceDict = params["nonce"] as? [String: Any],
                  let hidingArray = nonceDict["hiding"] as? [Any],
                  let bindingArray = nonceDict["binding"] as? [Any],
                  let hiding = arrayToData(hidingArray),
                  let binding = arrayToData(bindingArray) else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid nonce format"])
            }
            
            let nonce = SigningNonce(
                hiding: hiding,
                binding: binding
            )
            
            guard let commitmentDict = params["selfCommitment"] as? [String: Any],
                  let commitHidingArray = commitmentDict["hiding"] as? [Any],
                  let commitBindingArray = commitmentDict["binding"] as? [Any],
                  let commitHiding = arrayToData(commitHidingArray),
                  let commitBinding = arrayToData(commitBindingArray) else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid selfCommitment format"])
            }
            
            let selfCommitment = SigningCommitment(
                hiding: commitHiding,
                binding: commitBinding
            )
            
            guard let statechainCommitmentsDict = params["statechainCommitments"] as? [String: Any] else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid statechainCommitments format"])
            }
            
            var statechainCommitments: [String: SigningCommitment] = [:]
            
            for (key, value) in statechainCommitmentsDict {
                guard let commitDict = value as? [String: Any],
                      let hidingArray = commitDict["hiding"] as? [Any],
                      let bindingArray = commitDict["binding"] as? [Any],
                      let hiding = arrayToData(hidingArray),
                      let binding = arrayToData(bindingArray) else {
                    throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid statechain commitment format"])
                }
                
                statechainCommitments[key] = SigningCommitment(
                    hiding: hiding,
                    binding: binding
                )
            }
            
            let adaptorPubKey: Data?
            if let adaptorArray = params["adaptorPubKey"] as? [Any] {
                adaptorPubKey = arrayToData(adaptorArray)
            } else {
                adaptorPubKey = nil
            }
            
            let result = try signFrost(
                msg: msg,
                keyPackage: keyPackage,
                nonce: nonce,
                selfCommitment: selfCommitment,
                statechainCommitments: statechainCommitments,
                adaptorPublicKey: adaptorPubKey
            )
            
            resolve(dataToArray(result))
        } catch {
            reject("ERROR", error.localizedDescription, error)
        }
    }
    
    @objc(aggregateFrost:resolve:reject:)
    func rn_AggregateFrost(_ params: [String: Any],
                       resolve: @escaping RCTPromiseResolveBlock,
                       reject: @escaping RCTPromiseRejectBlock) {
        do {
            guard let msgArray = params["msg"] as? [Any],
                  let msg = arrayToData(msgArray) else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid msg format"])
            }
            
            // Parse statechain commitments
            guard let statechainCommitmentsDict = params["statechainCommitments"] as? [String: Any] else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid statechainCommitments format"])
            }
            
            var statechainCommitments: [String: SigningCommitment] = [:]
            for (key, value) in statechainCommitmentsDict {
                guard let commitDict = value as? [String: Any],
                      let hidingArray = commitDict["hiding"] as? [Any],
                      let bindingArray = commitDict["binding"] as? [Any],
                      let hiding = arrayToData(hidingArray),
                      let binding = arrayToData(bindingArray) else {
                    throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid statechain commitment format"])
                }
                statechainCommitments[key] = SigningCommitment(hiding: hiding, binding: binding)
            }
            
            // Parse self commitment
            guard let selfCommitmentDict = params["selfCommitment"] as? [String: Any],
                  let selfHidingArray = selfCommitmentDict["hiding"] as? [Any],
                  let selfBindingArray = selfCommitmentDict["binding"] as? [Any],
                  let selfHiding = arrayToData(selfHidingArray),
                  let selfBinding = arrayToData(selfBindingArray) else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid selfCommitment format"])
            }
            let selfCommitment = SigningCommitment(hiding: selfHiding, binding: selfBinding)
            
            // Parse statechain signatures
            guard let statechainSignaturesDict = params["statechainSignatures"] as? [String: Any] else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid statechainSignatures format"])
            }
            
            var statechainSignatures: [String: Data] = [:]
            for (key, value) in statechainSignaturesDict {
                guard let sigArray = value as? [Any],
                      let signature = arrayToData(sigArray) else {
                    throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid signature format"])
                }
                statechainSignatures[key] = signature
            }
            
            guard let statechainPublicKeysDict = params["statechainPublicKeys"] as? [String: Any] else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid statechainPublicKeys format"])
            }
            var statechainPublicKeys: [String: Data] = [:]
            for (key, value) in statechainPublicKeysDict {
                guard let keyArray = value as? [Any],
                    let keyData = arrayToData(keyArray) else {
                    throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid statechain public key format"])
                }
                statechainPublicKeys[key] = keyData
            }

            // Parse remaining parameters
            guard let selfSignatureArray = params["selfSignature"] as? [Any],
                  let selfSignature = arrayToData(selfSignatureArray),
                  let selfPublicKeyArray = params["selfPublicKey"] as? [Any],
                  let selfPublicKey = arrayToData(selfPublicKeyArray),
                  let verifyingKeyArray = params["verifyingKey"] as? [Any],
                  let verifyingKey = arrayToData(verifyingKeyArray) else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid parameter format"])
            }
            
            let adaptorPubKey: Data?
            if let adaptorArray = params["adaptorPubKey"] as? [Any] {
                adaptorPubKey = arrayToData(adaptorArray)
            } else {
                adaptorPubKey = nil
            }
            
            let result = try aggregateFrost(
                msg: msg,
                statechainCommitments: statechainCommitments,
                selfCommitment: selfCommitment,
                statechainSignatures: statechainSignatures,
                selfSignature: selfSignature,
                statechainPublicKeys: statechainPublicKeys,
                selfPublicKey: selfPublicKey,
                verifyingKey: verifyingKey,
                adaptorPublicKey: adaptorPubKey
            )
            
            resolve(dataToArray(result))
        } catch {
            reject("ERROR", error.localizedDescription, error)
        }
    }
    
    @objc(createDummyTx:resolve:reject:)
    func rn_createDummyTx(_ params: NSDictionary,
                       resolve: @escaping RCTPromiseResolveBlock,
                       reject: @escaping RCTPromiseRejectBlock) {
        print("SparkFrostModule.swift: createDummyTx called with params: \(params)")
        do {
            guard let address = params["address"] as? String else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Address is required"])
            }
            
            guard let amountSatsStr = params["amountSats"] as? String,
                  let amountSats = UInt64(amountSatsStr) else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid amountSats format"])
            }
            
            // Call the UniFFI-generated function
            let result = try createDummyTx(address: address, amountSats: amountSats)
            
            // Convert the result to a format that can be passed to JavaScript
            let resultDict: [String: Any] = [
                "tx": dataToArray(result.tx),
                "txid": result.txid
            ]
            
            print("SparkFrostModule.swift: About to resolve with: \(resultDict)")
            resolve(resultDict)
            print("SparkFrostModule.swift: Swift resolve was called.")
        } catch {
            print("SparkFrostModule.swift: Error in createDummyTx: \(error.localizedDescription)")
            reject("ERROR_CREATE_DUMMY_TX", error.localizedDescription, error)
        }
    }
    
    @objc(encryptEcies:resolve:reject:)
    func rn_encryptEcies(_ params: [String: Any],
                       resolve: @escaping RCTPromiseResolveBlock,
                       reject: @escaping RCTPromiseRejectBlock) {
        print("SparkFrostModule.swift: encryptEcies called with params: \(params)")
        do {
            guard let msgArray = params["msg"] as? [Any],
                  let msgData = arrayToData(msgArray) else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid msg format for encryptEcies"])
            }

            guard let publicKeyArray = params["publicKey"] as? [Any],
                  let publicKeyData = arrayToData(publicKeyArray) else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid publicKey format for encryptEcies"])
            }

            // Call the UniFFI-generated global function
            let resultData = try encryptEcies(msg: msgData, publicKey: publicKeyData)
            
            print("SparkFrostModule.swift: encryptEcies about to resolve")
            resolve(dataToArray(resultData)) // Convert result Data to [Int] for JS
            print("SparkFrostModule.swift: encryptEcies resolve was called.")
        } catch {
            print("SparkFrostModule.swift: Error in encryptEcies: \(error.localizedDescription)")
            reject("ERROR_ENCRYPT_ECIES", error.localizedDescription, error)
        }
    }

    @objc(decryptEcies:resolve:reject:)
    func rn_decryptEcies(_ params: [String: Any],
                       resolve: @escaping RCTPromiseResolveBlock,
                       reject: @escaping RCTPromiseRejectBlock) {
        print("SparkFrostModule.swift: decryptEcies called with params: \(params)")
        do {
            guard let encryptedMsgArray = params["encryptedMsg"] as? [Any],
                  let encryptedMsgData = arrayToData(encryptedMsgArray) else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid encryptedMsg format for decryptEcies"])
            }

            guard let privateKeyArray = params["privateKey"] as? [Any],
                  let privateKeyData = arrayToData(privateKeyArray) else {
                throw NSError(domain: "", code: -1, userInfo: [NSLocalizedDescriptionKey: "Invalid privateKey format for decryptEcies"])
            }

            // Call the UniFFI-generated global function
            let resultData = try decryptEcies(encryptedMsg: encryptedMsgData, privateKey: privateKeyData)
            
            print("SparkFrostModule.swift: decryptEcies about to resolve")
            resolve(dataToArray(resultData)) // Convert result Data to [Int] for JS
            print("SparkFrostModule.swift: decryptEcies resolve was called.")
        } catch {
            print("SparkFrostModule.swift: Error in decryptEcies: \(error.localizedDescription)")
            reject("ERROR_DECRYPT_ECIES", error.localizedDescription, error)
        }
    }
    
    @objc
    func constantsToExport() -> [AnyHashable : Any]! {
        return [:]
    }
}

private extension Array where Element == UInt8 {
    var data: Data {
        return Data(self)
    }
}
