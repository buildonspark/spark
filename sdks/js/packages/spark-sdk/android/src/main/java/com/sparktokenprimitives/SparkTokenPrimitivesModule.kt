package com.sparktokenprimitives

import com.facebook.react.bridge.*
import com.facebook.react.module.annotations.ReactModule
import uniffi.spark_token_primitives.*

@ReactModule(name = SparkTokenPrimitivesModule.NAME)
class SparkTokenPrimitivesModule(reactContext: ReactApplicationContext) : ReactContextBaseJavaModule(reactContext) {
    companion object {
        const val NAME = "SparkTokenPrimitivesModule"
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

    private fun ReadableMap.optionalByteArray(key: String): ByteArray? {
        if (!hasKey(key) || isNull(key)) return null
        return getArray(key)?.toByteArray()
    }

    private fun parseSelectedOutput(map: ReadableMap): SelectedTokenOutput {
        val previousTransactionHash = map.getArray("previousTransactionHash")?.toByteArray()
            ?: throw Exception("Invalid previousTransactionHash format")
        val previousTransactionVout = map.getInt("previousTransactionVout").toUInt()
        val ownerPublicKey = map.getArray("ownerPublicKey")?.toByteArray()
            ?: throw Exception("Invalid ownerPublicKey format")
        val tokenIdentifier = map.getArray("tokenIdentifier")?.toByteArray()
            ?: throw Exception("Invalid tokenIdentifier format")
        val tokenAmount = map.getArray("tokenAmount")?.toByteArray()
            ?: throw Exception("Invalid tokenAmount format")
        return SelectedTokenOutput(
            previousTransactionHash = previousTransactionHash,
            previousTransactionVout = previousTransactionVout,
            ownerPublicKey = ownerPublicKey,
            tokenIdentifier = tokenIdentifier,
            tokenAmount = tokenAmount
        )
    }

    private fun parseReceiverOutput(map: ReadableMap): ReceiverTokenOutput {
        val receiverSparkAddress = map.getString("receiverSparkAddress")
            ?: throw Exception("Invalid receiverSparkAddress format")
        val tokenIdentifier = map.optionalByteArray("tokenIdentifier")
        val tokenAmount = map.optionalByteArray("tokenAmount")
        return ReceiverTokenOutput(
            receiverSparkAddress = receiverSparkAddress,
            tokenIdentifier = tokenIdentifier,
            tokenAmount = tokenAmount
        )
    }

    private fun parseSignatureWithIndex(map: ReadableMap): SignatureWithIndexInput {
        val inputIndex = map.getInt("inputIndex").toUInt()
        val publicKey = map.getArray("publicKey")?.toByteArray()
            ?: throw Exception("Invalid publicKey format")
        val signature = map.getArray("signature")?.toByteArray()
            ?: throw Exception("Invalid signature format")
        return SignatureWithIndexInput(
            inputIndex = inputIndex,
            publicKey = publicKey,
            signature = signature
        )
    }

    @ReactMethod
    fun constructPartialTransferTransaction(params: ReadableMap, promise: Promise) {
        try {
            val identityPublicKey = params.getArray("identityPublicKey")?.toByteArray()
                ?: throw Exception("Invalid identityPublicKey format")

            val selectedOutputsArray = params.getArray("selectedOutputs")
                ?: throw Exception("Invalid selectedOutputs format")
            val selectedOutputs = mutableListOf<SelectedTokenOutput>()
            for (i in 0 until selectedOutputsArray.size()) {
                val map = selectedOutputsArray.getMap(i)
                    ?: throw Exception("Invalid selectedOutput at index $i")
                selectedOutputs.add(parseSelectedOutput(map))
            }

            val receiverOutputsArray = params.getArray("receiverOutputs")
                ?: throw Exception("Invalid receiverOutputs format")
            val receiverOutputs = mutableListOf<ReceiverTokenOutput>()
            for (i in 0 until receiverOutputsArray.size()) {
                val map = receiverOutputsArray.getMap(i)
                    ?: throw Exception("Invalid receiverOutput at index $i")
                receiverOutputs.add(parseReceiverOutput(map))
            }

            val operatorKeysArray = params.getArray("operatorIdentityPublicKeys")
                ?: throw Exception("Invalid operatorIdentityPublicKeys format")
            val operatorIdentityPublicKeys = mutableListOf<ByteArray>()
            for (i in 0 until operatorKeysArray.size()) {
                val keyArray = operatorKeysArray.getArray(i)
                    ?: throw Exception("Invalid operator key at index $i")
                operatorIdentityPublicKeys.add(keyArray.toByteArray())
            }

            val network = params.getInt("network").toUInt()
            val validityDurationSeconds = params.getDouble("validityDurationSeconds").toLong().toULong()
            val clientCreatedTimestampUnixMicros = params.getDouble("clientCreatedTimestampUnixMicros").toLong()
            val withdrawBondSats = params.getDouble("withdrawBondSats").toLong().toULong()
            val withdrawRelativeBlockLocktime = params.getDouble("withdrawRelativeBlockLocktime").toLong().toULong()
            val executeBeforeUnixMicros: Long? =
                if (params.hasKey("executeBeforeUnixMicros") && !params.isNull("executeBeforeUnixMicros"))
                    params.getDouble("executeBeforeUnixMicros").toLong()
                else null

            val request = TransferBuildRequest(
                identityPublicKey = identityPublicKey,
                selectedOutputs = selectedOutputs,
                receiverOutputs = receiverOutputs,
                operatorIdentityPublicKeys = operatorIdentityPublicKeys,
                network = network,
                validityDurationSeconds = validityDurationSeconds,
                clientCreatedTimestampUnixMicros = clientCreatedTimestampUnixMicros,
                withdrawBondSats = withdrawBondSats,
                withdrawRelativeBlockLocktime = withdrawRelativeBlockLocktime,
                executeBeforeUnixMicros = executeBeforeUnixMicros
            )

            val result = uniffi.spark_token_primitives.constructPartialTransferTransaction(request)

            val resultMap = Arguments.createMap().apply {
                putArray("partialTokenTransactionBytes", result.partialTokenTransactionBytes.toWritableArray())
                putArray("partialTokenTransactionHash", result.partialTokenTransactionHash.toWritableArray())
            }
            promise.resolve(resultMap)
        } catch (e: Exception) {
            promise.reject("ERROR_CONSTRUCT_PARTIAL_TRANSFER_TRANSACTION", e)
        }
    }

    @ReactMethod
    fun hashPartialTokenTransaction(params: ReadableMap, promise: Promise) {
        try {
            val bytes = params.getArray("partialTokenTransactionBytes")?.toByteArray()
                ?: throw Exception("Invalid partialTokenTransactionBytes format")
            val result = uniffi.spark_token_primitives.hashPartialTokenTransaction(bytes)
            promise.resolve(result.toWritableArray())
        } catch (e: Exception) {
            promise.reject("ERROR_HASH_PARTIAL_TOKEN_TRANSACTION", e)
        }
    }

    @ReactMethod
    fun buildBroadcastTransactionRequest(params: ReadableMap, promise: Promise) {
        try {
            val identityPublicKey = params.getArray("identityPublicKey")?.toByteArray()
                ?: throw Exception("Invalid identityPublicKey format")
            val partialTokenTransactionBytes = params.getArray("partialTokenTransactionBytes")?.toByteArray()
                ?: throw Exception("Invalid partialTokenTransactionBytes format")

            val signaturesArray = params.getArray("ownerSignatures")
                ?: throw Exception("Invalid ownerSignatures format")
            val ownerSignatures = mutableListOf<SignatureWithIndexInput>()
            for (i in 0 until signaturesArray.size()) {
                val map = signaturesArray.getMap(i)
                    ?: throw Exception("Invalid signature at index $i")
                ownerSignatures.add(parseSignatureWithIndex(map))
            }

            val request = BroadcastBuildRequest(
                identityPublicKey = identityPublicKey,
                partialTokenTransactionBytes = partialTokenTransactionBytes,
                ownerSignatures = ownerSignatures
            )

            val result = uniffi.spark_token_primitives.buildBroadcastTransactionRequest(request)
            promise.resolve(result.toWritableArray())
        } catch (e: Exception) {
            promise.reject("ERROR_BUILD_BROADCAST_TRANSACTION_REQUEST", e)
        }
    }

    @ReactMethod
    fun prepareTokenInvoice(params: ReadableMap, promise: Promise) {
        try {
            val receiverIdentityPublicKey = params.getArray("receiverIdentityPublicKey")?.toByteArray()
                ?: throw Exception("Invalid receiverIdentityPublicKey format")
            val network = params.getInt("network").toUInt()
            val tokenIdentifier = params.optionalByteArray("tokenIdentifier")
            val tokenAmount = params.optionalByteArray("tokenAmount")
            val memo = if (params.hasKey("memo") && !params.isNull("memo")) params.getString("memo") else null
            val senderSparkAddress = if (params.hasKey("senderSparkAddress") && !params.isNull("senderSparkAddress"))
                params.getString("senderSparkAddress") else null
            val expiryTimeUnixMillis: ULong? =
                if (params.hasKey("expiryTimeUnixMillis") && !params.isNull("expiryTimeUnixMillis"))
                    params.getDouble("expiryTimeUnixMillis").toLong().toULong()
                else null
            val invoiceId = params.optionalByteArray("invoiceId")

            val request = PrepareTokenInvoiceRequest(
                receiverIdentityPublicKey = receiverIdentityPublicKey,
                network = network,
                tokenIdentifier = tokenIdentifier,
                tokenAmount = tokenAmount,
                memo = memo,
                senderSparkAddress = senderSparkAddress,
                expiryTimeUnixMillis = expiryTimeUnixMillis,
                invoiceId = invoiceId
            )

            val result = uniffi.spark_token_primitives.prepareTokenInvoice(request)

            val resultMap = Arguments.createMap().apply {
                putArray("sparkInvoiceFieldsBytes", result.sparkInvoiceFieldsBytes.toWritableArray())
                putArray("sparkInvoiceHash", result.sparkInvoiceHash.toWritableArray())
                putString("unsignedSparkAddress", result.unsignedSparkAddress)
            }
            promise.resolve(resultMap)
        } catch (e: Exception) {
            promise.reject("ERROR_PREPARE_TOKEN_INVOICE", e)
        }
    }

    @ReactMethod
    fun finalizeTokenInvoice(params: ReadableMap, promise: Promise) {
        try {
            val receiverIdentityPublicKey = params.getArray("receiverIdentityPublicKey")?.toByteArray()
                ?: throw Exception("Invalid receiverIdentityPublicKey format")
            val network = params.getInt("network").toUInt()
            val sparkInvoiceFieldsBytes = params.getArray("sparkInvoiceFieldsBytes")?.toByteArray()
                ?: throw Exception("Invalid sparkInvoiceFieldsBytes format")
            val signature = params.optionalByteArray("signature")

            val request = FinalizeTokenInvoiceRequest(
                receiverIdentityPublicKey = receiverIdentityPublicKey,
                network = network,
                sparkInvoiceFieldsBytes = sparkInvoiceFieldsBytes,
                signature = signature
            )

            val result = uniffi.spark_token_primitives.finalizeTokenInvoice(request)
            promise.resolve(result)
        } catch (e: Exception) {
            promise.reject("ERROR_FINALIZE_TOKEN_INVOICE", e)
        }
    }
}
