package com.sparkfrost

import com.facebook.react.bridge.*
import com.facebook.react.module.annotations.ReactModule
import uniffi.spark_frost.*

@ReactModule(name = SparkFrostModule.NAME)
class SparkFrostModule(reactContext: ReactApplicationContext) : ReactContextBaseJavaModule(reactContext) {
    companion object {
        const val NAME = "SparkFrostModule"
    }

    override fun getName(): String = NAME

    private fun ReadableArray.toByteArray(): ByteArray {
        return this.toArrayList().map { (it as Number).toByte() }.toByteArray()
    }

    private fun ByteArray.toWritableArray(): WritableArray {
        val array = Arguments.createArray()
        this.forEach { array.pushInt(it.toInt()) }
        return array
    }

    @ReactMethod
    fun signFrost(params: ReadableMap, promise: Promise) {
        try {
            val msg = params.getArray("msg")?.toByteArray()
                ?: throw Exception("Invalid msg format")

            val keyPackageMap = params.getMap("keyPackage")
                ?: throw Exception("KeyPackage is required")

            val keyPackage = KeyPackage(
                secretKey = keyPackageMap.getArray("secretKey")?.toByteArray()
                    ?: throw Exception("Invalid secretKey format"),
                publicKey = keyPackageMap.getArray("publicKey")?.toByteArray()
                    ?: throw Exception("Invalid publicKey format"),
                verifyingKey = keyPackageMap.getArray("verifyingKey")?.toByteArray()
                    ?: throw Exception("Invalid verifyingKey format")
            )

            val nonceMap = params.getMap("nonce")
                ?: throw Exception("Nonce is required")
            val nonce = SigningNonce(
                hiding = nonceMap.getArray("hiding")?.toByteArray()
                    ?: throw Exception("Invalid nonce hiding format"),
                binding = nonceMap.getArray("binding")?.toByteArray()
                    ?: throw Exception("Invalid nonce binding format")
            )

            val commitmentMap = params.getMap("selfCommitment")
                ?: throw Exception("SelfCommitment is required")
            val selfCommitment = SigningCommitment(
                hiding = commitmentMap.getArray("hiding")?.toByteArray()
                    ?: throw Exception("Invalid commitment hiding format"),
                binding = commitmentMap.getArray("binding")?.toByteArray()
                    ?: throw Exception("Invalid commitment binding format")
            )

            val statechainCommitmentsMap = params.getMap("statechainCommitments")
                ?: throw Exception("StatechainCommitments is required")

            val statechainCommitments = mutableMapOf<String, SigningCommitment>()
            statechainCommitmentsMap.toHashMap().forEach { (key, value) ->
                val commitMap = value as? Map<*, *>
                    ?: throw Exception("Invalid statechain commitment format")

                val hidingArray = (commitMap["hiding"] as? List<*>)?.map { (it as Number).toByte() }?.toByteArray()
                    ?: throw Exception("Invalid statechain commitment hiding format")
                val bindingArray = (commitMap["binding"] as? List<*>)?.map { (it as Number).toByte() }?.toByteArray()
                    ?: throw Exception("Invalid statechain commitment binding format")

                statechainCommitments[key] = SigningCommitment(
                    hiding = hidingArray,
                    binding = bindingArray
                )
            }
            val adaptorPubKey = params.getArray("adaptorPubKey")?.toByteArray()

            val result = signFrost(
                msg = msg,
                keyPackage = keyPackage,
                nonce = nonce,
                selfCommitment = selfCommitment,
                statechainCommitments = statechainCommitments,
                adaptorPublicKey = adaptorPubKey
            )

            promise.resolve(result.toWritableArray())
        } catch (e: Exception) {
            promise.reject("ERROR", e)
        }
    }

    @ReactMethod
    fun aggregateFrost(params: ReadableMap, promise: Promise) {
        try {
            val msg = params.getArray("msg")?.toByteArray()
                ?: throw Exception("Invalid msg format")

            val statechainCommitmentsMap = params.getMap("statechainCommitments")
                ?: throw Exception("StatechainCommitments is required")
            val statechainCommitments = mutableMapOf<String, SigningCommitment>()
            statechainCommitmentsMap.toHashMap().forEach { (key, value) ->
                val commitMap = value as? Map<*, *>
                    ?: throw Exception("Invalid statechain commitment format")

                val hidingArray = (commitMap["hiding"] as? List<*>)?.map { (it as Number).toByte() }?.toByteArray()
                    ?: throw Exception("Invalid statechain commitment hiding format")
                val bindingArray = (commitMap["binding"] as? List<*>)?.map { (it as Number).toByte() }?.toByteArray()
                    ?: throw Exception("Invalid statechain commitment binding format")

                statechainCommitments[key] = SigningCommitment(
                    hiding = hidingArray,
                    binding = bindingArray
                )
            }

            val selfCommitmentMap = params.getMap("selfCommitment")
                ?: throw Exception("SelfCommitment is required")
            val selfCommitment = SigningCommitment(
                hiding = selfCommitmentMap.getArray("hiding")?.toByteArray()
                    ?: throw Exception("Invalid self commitment hiding format"),
                binding = selfCommitmentMap.getArray("binding")?.toByteArray()
                    ?: throw Exception("Invalid self commitment binding format")
            )

            val statechainSignaturesMap = params.getMap("statechainSignatures")
                ?: throw Exception("StatechainSignatures is required")
            val statechainSignatures = mutableMapOf<String, ByteArray>()
            statechainSignaturesMap.toHashMap().forEach { (key, value) ->
                val sigArray = (value as? List<*>)?.map { (it as Number).toByte() }?.toByteArray()
                    ?: throw Exception("Invalid statechain signature format")
                statechainSignatures[key] = sigArray
            }

            val selfSignature = params.getArray("selfSignature")?.toByteArray()
                ?: throw Exception("Invalid selfSignature format")

            val statechainPublicKeysMap = params.getMap("statechainPublicKeys")
                ?: throw Exception("StatechainPublicKeys is required")
            val statechainPublicKeys = mutableMapOf<String, ByteArray>()
            statechainPublicKeysMap.toHashMap().forEach { (key, value) ->
                val pubKeyArray = (value as? List<*>)?.map { (it as Number).toByte() }?.toByteArray()
                    ?: throw Exception("Invalid statechain public key format")
                statechainPublicKeys[key] = pubKeyArray
            }

            val selfPublicKey = params.getArray("selfPublicKey")?.toByteArray()
                ?: throw Exception("Invalid selfPublicKey format")

            val verifyingKey = params.getArray("verifyingKey")?.toByteArray()
                ?: throw Exception("Invalid verifyingKey format")

            val adaptorPubKey = params.getArray("adaptorPubKey")?.toByteArray()

            val result = aggregateFrost(
                msg = msg,
                statechainCommitments = statechainCommitments,
                selfCommitment = selfCommitment,
                statechainSignatures = statechainSignatures,
                selfSignature = selfSignature,
                statechainPublicKeys = statechainPublicKeys,
                selfPublicKey = selfPublicKey,
                verifyingKey = verifyingKey,
                adaptorPublicKey = adaptorPubKey
            )

            promise.resolve(result.toWritableArray())
        } catch (e: Exception) {
            promise.reject("ERROR", e)
        }
    }

    @ReactMethod
    fun createDummyTx(params: ReadableMap, promise: Promise) {
        try {
            val address = params.getString("address")
                ?: throw Exception("Address is required")
            val amountSats = params.getString("amountSats")
                ?.toULong()
                ?: throw Exception("Invalid amountSats format")

            val result = uniffi.spark_frost.createDummyTx(
                address = address,
                amountSats = amountSats
            )

            val map = Arguments.createMap().apply {
                putString("txid", result.txid)
                putArray("tx", result.tx.toWritableArray())
            }

            promise.resolve(map)
        } catch (e: Exception) {
            promise.reject("ERROR", e)
        }
    }

    @ReactMethod
    fun encryptEcies(params: ReadableMap, promise: Promise) {
        try {
            val msg = params.getArray("msg")?.toByteArray()
                ?: throw Exception("Invalid msg format")

            val publicKey = params.getArray("publicKey")?.toByteArray()
                ?: throw Exception("Invalid publicKey format")

            val result = uniffi.spark_frost.encryptEcies(
                msg = msg,
                publicKey = publicKey
            )

            promise.resolve(result.toWritableArray())
        } catch (e: Exception) {
            promise.reject("ERROR", e)
        }
    }

    @ReactMethod
    fun decryptEcies(params: ReadableMap, promise: Promise) {
        try {
            val encryptedMsg = params.getArray("encryptedMsg")?.toByteArray()
                ?: throw Exception("Invalid encryptedMsg format")

            val privateKey = params.getArray("privateKey")?.toByteArray()
                ?: throw Exception("Invalid privateKey format")

            val result = uniffi.spark_frost.decryptEcies(
                encryptedMsg = encryptedMsg,
                privateKey = privateKey
            )

            promise.resolve(result.toWritableArray())
        } catch (e: Exception) {
            promise.reject("ERROR", e)
        }
    }

    @ReactMethod
    fun getPublicKey(params: ReadableMap, promise: Promise) {
        try {
            val privateKey = params.getArray("privateKey")?.toByteArray()
                ?: throw Exception("Invalid privateKey format")

            val compressed = params.getBoolean("compressed")
                ?: throw Exception("Invalid compressed format")

            val result = uniffi.spark_frost.getPublicKeyBytes(
                privateKeyBytes = privateKey,
                compressed = compressed
            )

            promise.resolve(result.toWritableArray())
        } catch (e: Exception) {
            promise.reject("ERROR", e)
        }
    }

    @ReactMethod
    fun batchGetPublicKeys(params: ReadableMap, promise: Promise) {
        try {
            val privateKeysArray = params.getArray("privateKeys")
                ?: throw Exception("Invalid privateKeys format")
            
            val compressed = params.getBoolean("compressed")
                ?: throw Exception("Invalid compressed format")
            
            val resultsArray = Arguments.createArray()
            
            for (i in 0 until privateKeysArray.size()) {
                val privateKeyArray = privateKeysArray.getArray(i)
                    ?: throw Exception("Invalid privateKey format at index $i")
                
                val privateKey = privateKeyArray.toByteArray()
                
                val publicKey = uniffi.spark_frost.getPublicKeyBytes(
                    privateKeyBytes = privateKey,
                    compressed = compressed
                )
                
                resultsArray.pushArray(publicKey.toWritableArray())
            }
            
            promise.resolve(resultsArray)
        } catch (e: Exception) {
            promise.reject("ERROR", e)
        }
    }

    @ReactMethod
    fun getRandomPrivateKey(promise: Promise) {
        try {
            val result = uniffi.spark_frost.randomSecretKeyBytes()
            promise.resolve(result.toWritableArray())
        } catch (e: Exception) {
            promise.reject("ERROR", e)
        }
    }

    @ReactMethod
    fun verifySignature(params: ReadableMap, promise: Promise) {
        try {
            val signature = params.getArray("signature")?.toByteArray()
                ?: throw Exception("Invalid signature format")

            val message = params.getArray("message")?.toByteArray()
                ?: throw Exception("Invalid message format")

            val publicKey = params.getArray("publicKey")?.toByteArray()
                ?: throw Exception("Invalid publicKey format")

            val result = uniffi.spark_frost.verifySignatureBytes(
                signature = signature,
                message = message,
                pubkey = publicKey
            )

            promise.resolve(result)
        } catch (e: Exception) {
            promise.reject("ERROR", e)
        }
    }
}
