package com.spark

import android.util.Base64
import com.facebook.react.bridge.Arguments
import com.facebook.react.bridge.Promise
import com.facebook.react.bridge.ReactApplicationContext
import com.facebook.react.bridge.ReactContextBaseJavaModule
import com.facebook.react.bridge.ReactMethod
import com.facebook.react.bridge.ReadableArray
import com.facebook.react.bridge.ReadableMap
import com.facebook.react.bridge.WritableMap
import com.facebook.react.module.annotations.ReactModule
import io.grpc.CallOptions
import io.grpc.ClientCall
import io.grpc.ManagedChannel
import io.grpc.Metadata
import io.grpc.MethodDescriptor
import io.grpc.Status
import io.grpc.okhttp.OkHttpChannelBuilder
import java.io.ByteArrayInputStream
import java.io.InputStream
import java.net.URI
import java.util.Locale
import java.util.UUID
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors
import java.util.concurrent.LinkedBlockingQueue

@ReactModule(name = SparkGrpcModule.NAME)
class SparkGrpcModule(reactContext: ReactApplicationContext) :
    ReactContextBaseJavaModule(reactContext) {
    companion object {
        const val NAME = "SparkGrpcModule"
        private const val MAX_MESSAGE_SIZE = 20 * 1024 * 1024
    }

    private val channelByKey = ConcurrentHashMap<String, ManagedChannel>()
    private val unaryCallById = ConcurrentHashMap<String, ClientCall<ByteArray, ByteArray>>()
    private val cancelledUnaryCallIds = ConcurrentHashMap.newKeySet<String>()
    private val streamById = ConcurrentHashMap<String, StreamState>()
    private val executor = Executors.newCachedThreadPool()

    private data class StreamEvent(
        val kind: String,
        val metadata: Metadata? = null,
        val message: ByteArray? = null,
        val status: Status? = null,
        val trailers: Metadata? = null,
    )

    private class StreamState(
        val streamId: String,
        val events: LinkedBlockingQueue<StreamEvent> = LinkedBlockingQueue(),
    ) {
        @Volatile
        private var call: ClientCall<ByteArray, ByteArray>? = null
        private var cancelled = false

        fun setCall(newCall: ClientCall<ByteArray, ByteArray>): Boolean {
            val shouldCancel = synchronized(this) {
                if (cancelled) {
                    true
                } else {
                    call = newCall
                    false
                }
            }

            if (shouldCancel) {
                newCall.cancel("Stream cancelled", null)
                return false
            }
            return true
        }

        fun requestNext() {
            call?.request(1)
        }

        fun cancelCall(description: String, cause: Throwable? = null) {
            val currentCall = synchronized(this) {
                cancelled = true
                call
            }
            currentCall?.cancel(description, cause)
        }
    }

    private object ByteArrayMarshaller : MethodDescriptor.Marshaller<ByteArray> {
        override fun stream(value: ByteArray): InputStream = ByteArrayInputStream(value)

        override fun parse(stream: InputStream): ByteArray = stream.readBytes()
    }

    override fun getName(): String = NAME

    override fun invalidate() {
        unaryCallById.values.forEach { call ->
            call.cancel("Module invalidated", null)
        }
        unaryCallById.clear()
        cancelledUnaryCallIds.clear()

        streamById.values.forEach { streamState ->
            streamState.cancelCall("Module invalidated")
            streamState.events.offer(
                StreamEvent(
                    kind = "complete",
                    status = Status.CANCELLED.withDescription("Module invalidated"),
                    trailers = Metadata(),
                ),
            )
        }
        streamById.clear()

        channelByKey.values.forEach { channel ->
            channel.shutdownNow()
        }
        channelByKey.clear()

        executor.shutdownNow()
        super.invalidate()
    }

    @ReactMethod
    fun grpcUnaryCall(params: ReadableMap, promise: Promise) {
        executor.execute {
            try {
                val address = requireString(params, "address")
                val isStreamClientType = params.getBoolean("isStreamClientType")
                val path = requireString(params, "path")
                val requestBase64 = requireString(params, "requestBase64")
                val metadataMap = params.getMap("metadata")
                val requestId = optionalString(params, "requestId")

                val channel = getOrCreateChannel(address, isStreamClientType)
                val methodDescriptor = buildMethodDescriptor(path, false)
                val requestBytes = Base64.decode(requestBase64, Base64.DEFAULT)
                val requestMetadata = readableMapToMetadata(metadataMap)

                val latch = CountDownLatch(1)
                var responseBytes: ByteArray? = null
                var headers: Metadata? = null
                var trailers: Metadata? = null
                var status: Status = Status.UNKNOWN

                val call = channel.newCall(methodDescriptor, CallOptions.DEFAULT)
                if (requestId != null) {
                    unaryCallById[requestId] = call
                    if (cancelledUnaryCallIds.remove(requestId)) {
                        unaryCallById.remove(requestId, call)
                        call.cancel("Cancelled from JS", null)
                        promise.reject("NATIVE_GRPC_UNARY_CANCELLED", "Unary call cancelled")
                        return@execute
                    }
                }
                try {
                    call.start(
                        object : ClientCall.Listener<ByteArray>() {
                            override fun onHeaders(headerMetadata: Metadata) {
                                headers = headerMetadata
                            }

                            override fun onMessage(message: ByteArray) {
                                responseBytes = message
                            }

                            override fun onClose(closeStatus: Status, trailerMetadata: Metadata) {
                                status = closeStatus
                                trailers = trailerMetadata
                                latch.countDown()
                            }
                        },
                        requestMetadata,
                    )

                    call.request(1)
                    call.sendMessage(requestBytes)
                    call.halfClose()
                    latch.await()
                } finally {
                    if (requestId != null) {
                        unaryCallById.remove(requestId)
                        cancelledUnaryCallIds.remove(requestId)
                    }
                }

                val responseMap = Arguments.createMap().apply {
                    putInt("statusCode", status.code.value())
                    putString("statusMessage", status.description ?: "")
                    putString(
                        "responseBase64",
                        Base64.encodeToString(responseBytes ?: ByteArray(0), Base64.NO_WRAP),
                    )
                    putMap("headers", metadataToWritableMap(headers))
                    putMap("trailers", metadataToWritableMap(trailers))
                }
                promise.resolve(responseMap)
            } catch (e: Exception) {
                promise.reject("NATIVE_GRPC_UNARY_ERROR", e.message, e)
            }
        }
    }

    @ReactMethod
    fun grpcUnaryCallCancel(params: ReadableMap, promise: Promise) {
        executor.execute {
            try {
                val requestId = optionalString(params, "requestId")
                if (requestId != null) {
                    val call = unaryCallById.remove(requestId)
                    if (call != null) {
                        call.cancel("Cancelled from JS", null)
                    } else {
                        cancelledUnaryCallIds.add(requestId)
                    }
                }
                promise.resolve(null)
            } catch (e: Exception) {
                promise.reject("NATIVE_GRPC_UNARY_CANCEL_ERROR", e.message, e)
            }
        }
    }

    @ReactMethod
    fun grpcServerStreamStart(params: ReadableMap, promise: Promise) {
        executor.execute {
            var streamId: String? = null
            var streamState: StreamState? = null
            var startResolved = false
            try {
                val address = requireString(params, "address")
                val isStreamClientType = params.getBoolean("isStreamClientType")
                val path = requireString(params, "path")
                val requestBase64 = requireString(params, "requestBase64")
                val metadataMap = params.getMap("metadata")

                val newStreamId = UUID.randomUUID().toString()
                val newStreamState = StreamState(newStreamId)
                streamId = newStreamId
                streamState = newStreamState
                streamById[newStreamId] = newStreamState

                val channel = getOrCreateChannel(address, isStreamClientType)
                val methodDescriptor = buildMethodDescriptor(path, true)
                val requestBytes = Base64.decode(requestBase64, Base64.DEFAULT)
                val requestMetadata = readableMapToMetadata(metadataMap)

                val call = channel.newCall(methodDescriptor, CallOptions.DEFAULT)
                if (!newStreamState.setCall(call)) {
                    streamById.remove(newStreamId)
                    promise.reject(
                        "NATIVE_GRPC_STREAM_START_CANCELLED",
                        "Stream start cancelled",
                    )
                    return@execute
                }

                val responseMap = Arguments.createMap().apply {
                    putString("streamId", newStreamId)
                }
                promise.resolve(responseMap)
                startResolved = true

                call.start(
                    object : ClientCall.Listener<ByteArray>() {
                        override fun onHeaders(headerMetadata: Metadata) {
                            newStreamState.events.offer(
                                StreamEvent(
                                    kind = "headers",
                                    metadata = headerMetadata,
                                ),
                            )
                        }

                        override fun onMessage(message: ByteArray) {
                            newStreamState.events.offer(
                                StreamEvent(
                                    kind = "message",
                                    message = message,
                                ),
                            )
                        }

                        override fun onClose(closeStatus: Status, trailerMetadata: Metadata) {
                            newStreamState.events.offer(
                                StreamEvent(
                                    kind = "complete",
                                    status = closeStatus,
                                    trailers = trailerMetadata,
                                ),
                            )
                        }
                    },
                    requestMetadata,
                )

                call.request(1)
                call.sendMessage(requestBytes)
                call.halfClose()
            } catch (e: Exception) {
                streamState?.cancelCall("Stream start failed", e)
                if (startResolved) {
                    streamState?.events?.offer(
                        StreamEvent(
                            kind = "complete",
                            status = Status.UNKNOWN.withDescription(e.message ?: "Stream start failed"),
                            trailers = Metadata(),
                        ),
                    )
                } else {
                    streamId?.let(streamById::remove)
                    promise.reject("NATIVE_GRPC_STREAM_START_ERROR", e.message, e)
                }
            }
        }
    }

    @ReactMethod
    fun grpcServerStreamNext(params: ReadableMap, promise: Promise) {
        executor.execute {
            try {
                val streamId = requireString(params, "streamId")
                val streamState = streamById[streamId]
                if (streamState == null) {
                    promise.resolve(
                        Arguments.createMap().apply {
                            putString("kind", "complete")
                            putInt("statusCode", Status.CANCELLED.code.value())
                            putString("statusMessage", "Stream not found")
                            putMap("trailers", Arguments.createMap())
                        },
                    )
                    return@execute
                }

                val event = streamState.events.take()
                val eventMap = streamEventToWritableMap(event)
                if (event.kind == "message") {
                    streamState.requestNext()
                }
                if (event.kind == "complete") {
                    streamById.remove(streamId)
                }
                promise.resolve(eventMap)
            } catch (e: Exception) {
                promise.reject("NATIVE_GRPC_STREAM_NEXT_ERROR", e.message, e)
            }
        }
    }

    @ReactMethod
    fun grpcServerStreamCancel(params: ReadableMap, promise: Promise) {
        executor.execute {
            try {
                val streamId = requireString(params, "streamId")
                val streamState = streamById.remove(streamId)
                if (streamState != null) {
                    streamState.cancelCall("Cancelled from JS")
                    streamState.events.offer(
                        StreamEvent(
                            kind = "complete",
                            status = Status.CANCELLED.withDescription("Cancelled from JS"),
                            trailers = Metadata(),
                        ),
                    )
                }
                promise.resolve(null)
            } catch (e: Exception) {
                promise.reject("NATIVE_GRPC_STREAM_CANCEL_ERROR", e.message, e)
            }
        }
    }

    @ReactMethod
    fun grpcCloseChannel(params: ReadableMap, promise: Promise) {
        executor.execute {
            try {
                val address = requireString(params, "address")
                val isStreamClientType = params.getBoolean("isStreamClientType")
                val channel = channelByKey.remove(channelKey(address, isStreamClientType))
                channel?.shutdownNow()
                promise.resolve(null)
            } catch (e: Exception) {
                promise.reject("NATIVE_GRPC_CLOSE_CHANNEL_ERROR", e.message, e)
            }
        }
    }

    private fun streamEventToWritableMap(event: StreamEvent): WritableMap {
        return Arguments.createMap().apply {
            putString("kind", event.kind)
            when (event.kind) {
                "headers" -> {
                    putMap("metadata", metadataToWritableMap(event.metadata))
                }

                "message" -> {
                    putString(
                        "messageBase64",
                        Base64.encodeToString(event.message ?: ByteArray(0), Base64.NO_WRAP),
                    )
                }

                "complete" -> {
                    val status = event.status ?: Status.UNKNOWN
                    putInt("statusCode", status.code.value())
                    putString("statusMessage", status.description ?: "")
                    putMap("trailers", metadataToWritableMap(event.trailers))
                }
            }
        }
    }

    private fun buildMethodDescriptor(
        path: String,
        isServerStreaming: Boolean,
    ): MethodDescriptor<ByteArray, ByteArray> {
        val cleanedPath = path.removePrefix("/")
        val segments = cleanedPath.split("/", limit = 2)
        if (segments.size != 2) {
            throw IllegalArgumentException("Invalid method path: $path")
        }
        val serviceName = segments[0]
        val methodName = segments[1]

        return MethodDescriptor.newBuilder<ByteArray, ByteArray>()
            .setType(
                if (isServerStreaming) {
                    MethodDescriptor.MethodType.SERVER_STREAMING
                } else {
                    MethodDescriptor.MethodType.UNARY
                },
            )
            .setFullMethodName(
                MethodDescriptor.generateFullMethodName(serviceName, methodName),
            )
            .setRequestMarshaller(ByteArrayMarshaller)
            .setResponseMarshaller(ByteArrayMarshaller)
            .build()
    }

    private fun requireString(map: ReadableMap, key: String): String {
        return map.getString(key)
            ?: throw IllegalArgumentException("Missing required field: $key")
    }

    private fun optionalString(map: ReadableMap, key: String): String? {
        if (!map.hasKey(key) || map.isNull(key)) {
            return null
        }
        return map.getString(key)?.takeIf { it.isNotEmpty() }
    }

    private fun channelKey(address: String, isStreamClientType: Boolean): String {
        val clientType = if (isStreamClientType) "stream" else "unary"
        return "$address|$clientType"
    }

    private fun getOrCreateChannel(
        address: String,
        isStreamClientType: Boolean,
    ): ManagedChannel {
        return channelByKey.computeIfAbsent(channelKey(address, isStreamClientType)) {
            createChannel(address)
        }
    }

    private fun createChannel(address: String): ManagedChannel {
        val uri = URI(address)
        val scheme = (uri.scheme ?: "https").lowercase(Locale.US)
        val host = uri.host ?: throw IllegalArgumentException("Invalid gRPC address host: $address")
        val port = if (uri.port != -1) uri.port else if (scheme == "http") 80 else 443

        val builder = OkHttpChannelBuilder
            .forAddress(host, port)
            .maxInboundMessageSize(MAX_MESSAGE_SIZE)

        if (scheme == "http") {
            builder.usePlaintext()
        } else {
            builder.useTransportSecurity()
        }

        return builder.build()
    }

    private fun readableMapToMetadata(map: ReadableMap?): Metadata {
        val metadata = Metadata()
        if (map == null) {
            return metadata
        }

        val iterator = map.keySetIterator()
        while (iterator.hasNextKey()) {
            val rawKey = iterator.nextKey()
            val key = rawKey.lowercase(Locale.US)
            val values = map.getArray(rawKey) ?: continue
            appendMetadataValues(metadata, key, values)
        }
        return metadata
    }

    private fun appendMetadataValues(metadata: Metadata, key: String, values: ReadableArray) {
        if (!isValidMetadataKey(key)) {
            return
        }

        if (key.endsWith(Metadata.BINARY_HEADER_SUFFIX)) {
            val metadataKey = Metadata.Key.of(key, Metadata.BINARY_BYTE_MARSHALLER)
            for (index in 0 until values.size()) {
                val value = values.getString(index) ?: continue
                metadata.put(metadataKey, Base64.decode(value, Base64.DEFAULT))
            }
        } else {
            val metadataKey = Metadata.Key.of(key, Metadata.ASCII_STRING_MARSHALLER)
            for (index in 0 until values.size()) {
                val value = values.getString(index) ?: continue
                metadata.put(metadataKey, value)
            }
        }
    }

    private fun isValidMetadataKey(key: String): Boolean {
        if (key.isEmpty()) {
            return false
        }

        return key.all { char ->
            char in '0'..'9' ||
                char in 'a'..'z' ||
                char == '_' ||
                char == '.' ||
                char == '-'
        }
    }

    private fun metadataToWritableMap(metadata: Metadata?): WritableMap {
        val map = Arguments.createMap()
        if (metadata == null) {
            return map
        }

        for (rawKey in metadata.keys()) {
            val key = rawKey.lowercase(Locale.US)
            if (!isValidMetadataKey(key)) {
                continue
            }

            val values = mutableListOf<String>()
            if (key.endsWith(Metadata.BINARY_HEADER_SUFFIX)) {
                val metadataKey = Metadata.Key.of(key, Metadata.BINARY_BYTE_MARSHALLER)
                metadata.getAll(metadataKey)?.forEach { bytes ->
                    values.add(Base64.encodeToString(bytes, Base64.NO_WRAP))
                }
            } else {
                val metadataKey = Metadata.Key.of(key, Metadata.ASCII_STRING_MARSHALLER)
                metadata.getAll(metadataKey)?.forEach { value ->
                    values.add(value)
                }
            }

            if (values.isEmpty()) {
                continue
            }

            val array = Arguments.createArray()
            values.forEach(array::pushString)
            map.putArray(key, array)
        }

        return map
    }
}
