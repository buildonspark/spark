import Foundation
import GRPC
import NIOCore
import NIOHPACK
import NIOTransportServices
import React

@objc(SparkGrpcModule)
class SparkGrpcModule: NSObject, RCTBridgeModule, RCTInvalidating {
    private enum NativeGrpcError: LocalizedError {
        case missingRequiredField(String)
        case invalidAddress(String)

        var errorDescription: String? {
            switch self {
            case .missingRequiredField(let field):
                return "Missing required field: \(field)"
            case .invalidAddress(let address):
                return "Invalid gRPC address: \(address)"
            }
        }
    }

    private struct ParsedAddress {
        let host: String
        let port: Int
        let useTLS: Bool
    }

    private enum StreamEvent {
        case headers([String: [String]])
        case message(String)
        case complete(statusCode: Int, statusMessage: String, trailers: [String: [String]])

        var isComplete: Bool {
            if case .complete = self {
                return true
            }
            return false
        }

        func asDictionary() -> [String: Any] {
            switch self {
            case .headers(let metadata):
                return [
                    "kind": "headers",
                    "metadata": metadata,
                ]
            case .message(let messageBase64):
                return [
                    "kind": "message",
                    "messageBase64": messageBase64,
                ]
            case .complete(let statusCode, let statusMessage, let trailers):
                return [
                    "kind": "complete",
                    "statusCode": statusCode,
                    "statusMessage": statusMessage,
                    "trailers": trailers,
                ]
            }
        }
    }

    private final class RawPayload: GRPCPayload {
        let bytes: [UInt8]

        init(bytes: [UInt8]) {
            self.bytes = bytes
        }

        init(data: Data) {
            self.bytes = [UInt8](data)
        }

        init(serializedByteBuffer: inout ByteBuffer) throws {
            self.bytes = serializedByteBuffer.readBytes(length: serializedByteBuffer.readableBytes) ?? []
        }

        func serialize(into buffer: inout ByteBuffer) throws {
            buffer.writeBytes(self.bytes)
        }

        var data: Data {
            Data(self.bytes)
        }
    }

    private final class StreamState {
        let streamId: String
        private let condition = NSCondition()
        private var events: [StreamEvent] = []
        private var bufferedMessages: [StreamEvent] = []
        private var call: ServerStreamingCall<RawPayload, RawPayload>?
        private var cancelled = false
        private var receivedHeaders = false

        init(streamId: String) {
            self.streamId = streamId
        }

        func setCall(_ call: ServerStreamingCall<RawPayload, RawPayload>) -> Bool {
            self.condition.lock()
            if self.cancelled {
                self.condition.unlock()
                call.cancel(promise: nil)
                return false
            }
            self.call = call
            self.condition.unlock()
            return true
        }

        func cancelCall() {
            self.condition.lock()
            self.cancelled = true
            let currentCall = self.call
            self.condition.unlock()
            currentCall?.cancel(promise: nil)
        }

        func pushHeaders(_ metadata: [String: [String]]) {
            self.condition.lock()
            self.receivedHeaders = true
            self.events.append(.headers(metadata))
            self.events.append(contentsOf: self.bufferedMessages)
            self.bufferedMessages.removeAll()
            self.condition.signal()
            self.condition.unlock()
        }

        func pushMessage(_ messageBase64: String) {
            self.condition.lock()
            let event = StreamEvent.message(messageBase64)
            if self.receivedHeaders {
                self.events.append(event)
                self.condition.signal()
            } else {
                self.bufferedMessages.append(event)
            }
            self.condition.unlock()
        }

        func pushComplete(_ event: StreamEvent) {
            self.condition.lock()
            if !self.receivedHeaders {
                self.events.append(contentsOf: self.bufferedMessages)
                self.bufferedMessages.removeAll()
            }
            self.events.append(event)
            self.condition.signal()
            self.condition.unlock()
        }

        func pop() -> StreamEvent {
            self.condition.lock()
            while self.events.isEmpty {
                self.condition.wait()
            }
            let event = self.events.removeFirst()
            self.condition.unlock()
            return event
        }
    }

    private static let workerQueue = DispatchQueue(
        label: "com.spark.grpc.worker",
        qos: .userInitiated,
        attributes: .concurrent
    )
    private static let stateLock = NSLock()
    private static var channelByKey: [String: ClientConnection] = [:]
    private static var unaryCallById: [String: UnaryCall<RawPayload, RawPayload>] = [:]
    private static var cancelledUnaryCallIds: Set<String> = []
    private static var streamById: [String: StreamState] = [:]
    private static var eventLoopGroup: NIOTSEventLoopGroup? = NIOTSEventLoopGroup(loopCount: 1)
    private static let maxMessageSize = 20 * 1024 * 1024
    private static let validMetadataKeyCharacters = CharacterSet(
        charactersIn: "abcdefghijklmnopqrstuvwxyz0123456789-_."
    )

    @objc
    static func moduleName() -> String! {
        "SparkGrpcModule"
    }

    @objc
    static func requiresMainQueueSetup() -> Bool {
        false
    }

    @objc
    func invalidate() {
        Self.cancelActiveUnaryCalls()
        Self.completeActiveStreams(statusMessage: "SparkGrpcModule invalidated")
        Self.workerQueue.async(flags: .barrier) {
            Self.teardownResources()
        }
    }

    @objc(grpcUnaryCall:resolve:reject:)
    func grpcUnaryCall(
        _ params: NSDictionary,
        resolve: @escaping RCTPromiseResolveBlock,
        reject: @escaping RCTPromiseRejectBlock
    ) {
        Self.workerQueue.async {
            do {
                let address = try Self.requireString(params, key: "address")
                let path = try Self.requireString(params, key: "path")
                let requestBase64 = try Self.requireString(params, key: "requestBase64")
                let isStreamClientType = (params["isStreamClientType"] as? NSNumber)?.boolValue ?? false
                let metadata = params["metadata"] as? NSDictionary
                let requestId = params["requestId"] as? String

                let channel = try Self.getOrCreateChannel(
                    address: address,
                    isStreamClientType: isStreamClientType
                )
                let callOptions = CallOptions(customMetadata: Self.metadataFromDictionary(metadata))
                let requestPayload = RawPayload(data: Self.decodeBase64(requestBase64))
                let call = channel.makeUnaryCall(
                    path: path,
                    request: requestPayload,
                    callOptions: callOptions
                ) as UnaryCall<RawPayload, RawPayload>
                if let requestId = requestId, !requestId.isEmpty {
                    guard Self.insertUnaryCall(call, requestId: requestId) else {
                        call.cancel(promise: nil)
                        reject("NATIVE_GRPC_UNARY_CANCELLED", "Unary call cancelled", nil)
                        return
                    }
                }
                defer {
                    if let requestId = requestId, !requestId.isEmpty {
                        _ = Self.removeUnaryCall(requestId)
                    }
                }

                let responsePayload: RawPayload
                do {
                    responsePayload = try call.response.wait()
                } catch {
                    responsePayload = RawPayload(bytes: [])
                }

                let status: GRPCStatus
                do {
                    status = try call.status.wait()
                } catch {
                    status = Self.grpcStatus(from: error)
                }

                let headers = try? call.initialMetadata.wait()
                let trailers = try? call.trailingMetadata.wait()

                resolve([
                    "statusCode": status.code.rawValue,
                    "statusMessage": status.message ?? "",
                    "responseBase64": responsePayload.data.base64EncodedString(),
                    "headers": Self.dictionaryFromMetadata(headers),
                    "trailers": Self.dictionaryFromMetadata(trailers),
                ])
            } catch {
                reject("NATIVE_GRPC_UNARY_ERROR", Self.errorMessage(for: error), Self.asNSError(error))
            }
        }
    }

    @objc(grpcUnaryCallCancel:resolve:reject:)
    func grpcUnaryCallCancel(
        _ params: NSDictionary,
        resolve: @escaping RCTPromiseResolveBlock,
        reject: @escaping RCTPromiseRejectBlock
    ) {
        Self.workerQueue.async {
            do {
                let requestId = try Self.requireString(params, key: "requestId")
                Self.cancelUnaryCall(requestId)?.cancel(promise: nil)
                resolve(nil)
            } catch {
                reject("NATIVE_GRPC_UNARY_CANCEL_ERROR", Self.errorMessage(for: error), Self.asNSError(error))
            }
        }
    }

    @objc(grpcServerStreamStart:resolve:reject:)
    func grpcServerStreamStart(
        _ params: NSDictionary,
        resolve: @escaping RCTPromiseResolveBlock,
        reject: @escaping RCTPromiseRejectBlock
    ) {
        Self.workerQueue.async {
            do {
                let address = try Self.requireString(params, key: "address")
                let path = try Self.requireString(params, key: "path")
                let requestBase64 = try Self.requireString(params, key: "requestBase64")
                let isStreamClientType = (params["isStreamClientType"] as? NSNumber)?.boolValue ?? false
                let metadata = params["metadata"] as? NSDictionary

                let streamId = UUID().uuidString
                let streamState = StreamState(streamId: streamId)
                Self.insertStreamState(streamState)

                resolve(["streamId": streamId])

                Self.workerQueue.async {
                    guard Self.hasEventLoopGroup() else {
                        streamState.pushComplete(
                            .complete(
                                statusCode: 1,
                                statusMessage: "SparkGrpcModule invalidated",
                                trailers: [:]
                            )
                        )
                        return
                    }

                    do {
                        let channel = try Self.getOrCreateChannel(
                            address: address,
                            isStreamClientType: isStreamClientType
                        )
                        let callOptions = CallOptions(customMetadata: Self.metadataFromDictionary(metadata))
                        let requestPayload = RawPayload(data: Self.decodeBase64(requestBase64))

                        let call = channel.makeServerStreamingCall(
                            path: path,
                            request: requestPayload,
                            callOptions: callOptions
                        ) { response in
                            streamState.pushMessage(response.data.base64EncodedString())
                        } as ServerStreamingCall<RawPayload, RawPayload>

                        guard streamState.setCall(call) else {
                            return
                        }

                        if let initialMetadata = try? call.initialMetadata.wait() {
                            streamState.pushHeaders(Self.dictionaryFromMetadata(initialMetadata))
                        }

                        let status: GRPCStatus
                        do {
                            status = try call.status.wait()
                        } catch {
                            streamState.pushComplete(Self.completeEvent(from: error, trailers: nil))
                            return
                        }

                        let trailers = try? call.trailingMetadata.wait()
                        streamState.pushComplete(
                            .complete(
                                statusCode: status.code.rawValue,
                                statusMessage: status.message ?? "",
                                trailers: Self.dictionaryFromMetadata(trailers)
                            )
                        )
                    } catch {
                        streamState.pushComplete(Self.completeEvent(from: error, trailers: nil))
                    }
                }
            } catch {
                reject("NATIVE_GRPC_STREAM_START_ERROR", Self.errorMessage(for: error), Self.asNSError(error))
            }
        }
    }

    @objc(grpcServerStreamNext:resolve:reject:)
    func grpcServerStreamNext(
        _ params: NSDictionary,
        resolve: @escaping RCTPromiseResolveBlock,
        reject: @escaping RCTPromiseRejectBlock
    ) {
        Self.workerQueue.async {
            do {
                let streamId = try Self.requireString(params, key: "streamId")
                guard let streamState = Self.getStreamState(streamId) else {
                    resolve([
                        "kind": "complete",
                        "statusCode": 1,
                        "statusMessage": "Stream not found",
                        "trailers": [String: [String]](),
                    ])
                    return
                }

                let event = streamState.pop()
                if event.isComplete {
                    _ = Self.removeStreamState(streamId)
                }
                resolve(event.asDictionary())
            } catch {
                reject("NATIVE_GRPC_STREAM_NEXT_ERROR", Self.errorMessage(for: error), Self.asNSError(error))
            }
        }
    }

    @objc(grpcServerStreamCancel:resolve:reject:)
    func grpcServerStreamCancel(
        _ params: NSDictionary,
        resolve: @escaping RCTPromiseResolveBlock,
        reject: @escaping RCTPromiseRejectBlock
    ) {
        Self.workerQueue.async {
            do {
                guard let streamIdValue = params["streamId"] as? String, !streamIdValue.isEmpty else {
                    resolve(nil)
                    return
                }

                if let streamState = Self.getStreamState(streamIdValue) {
                    streamState.cancelCall()
                    streamState.pushComplete(
                        .complete(
                            statusCode: 1,
                            statusMessage: "Cancelled from JS",
                            trailers: [:]
                        )
                    )
                }

                _ = Self.removeStreamState(streamIdValue)
                resolve(nil)
            } catch {
                reject("NATIVE_GRPC_STREAM_CANCEL_ERROR", Self.errorMessage(for: error), Self.asNSError(error))
            }
        }
    }

    @objc(grpcCloseChannel:resolve:reject:)
    func grpcCloseChannel(
        _ params: NSDictionary,
        resolve: @escaping RCTPromiseResolveBlock,
        reject: @escaping RCTPromiseRejectBlock
    ) {
        Self.workerQueue.async {
            do {
                guard let address = params["address"] as? String, !address.isEmpty else {
                    resolve(nil)
                    return
                }
                let isStreamClientType = (params["isStreamClientType"] as? NSNumber)?.boolValue ?? false

                if let channel = Self.removeChannel(
                    address: address,
                    isStreamClientType: isStreamClientType
                ) {
                    _ = try? channel.close().wait()
                }

                resolve(nil)
            } catch {
                reject("NATIVE_GRPC_CLOSE_CHANNEL_ERROR", Self.errorMessage(for: error), Self.asNSError(error))
            }
        }
    }

    private static func parseAddress(_ address: String) throws -> ParsedAddress {
        let normalizedAddress = address.contains("://") ? address : "https://\(address)"
        guard let components = URLComponents(string: normalizedAddress) else {
            throw NativeGrpcError.invalidAddress(address)
        }

        let scheme = (components.scheme ?? "https").lowercased()
        guard let host = components.host, !host.isEmpty else {
            throw NativeGrpcError.invalidAddress(address)
        }

        let port = components.port ?? (scheme == "http" ? 80 : 443)
        return ParsedAddress(host: host, port: port, useTLS: scheme != "http")
    }

    private static func createChannel(address: String) throws -> ClientConnection {
        let parsedAddress = try parseAddress(address)
        let eventLoopGroup = getOrCreateEventLoopGroup()
        if parsedAddress.useTLS {
            return ClientConnection.usingPlatformAppropriateTLS(for: eventLoopGroup)
                .withMaximumReceiveMessageLength(maxMessageSize)
                .connect(host: parsedAddress.host, port: parsedAddress.port)
        } else {
            return ClientConnection.insecure(group: eventLoopGroup)
                .withMaximumReceiveMessageLength(maxMessageSize)
                .connect(host: parsedAddress.host, port: parsedAddress.port)
        }
    }

    private static func channelKey(address: String, isStreamClientType: Bool) -> String {
        let clientType = isStreamClientType ? "stream" : "unary"
        return "\(address)|\(clientType)"
    }

    private static func getOrCreateChannel(
        address: String,
        isStreamClientType: Bool
    ) throws -> ClientConnection {
        let key = channelKey(address: address, isStreamClientType: isStreamClientType)
        stateLock.lock()
        if let existingChannel = channelByKey[key] {
            stateLock.unlock()
            return existingChannel
        }
        stateLock.unlock()

        let newChannel = try createChannel(address: address)
        stateLock.lock()
        if let existingChannel = channelByKey[key] {
            stateLock.unlock()
            _ = try? newChannel.close().wait()
            return existingChannel
        }
        channelByKey[key] = newChannel
        stateLock.unlock()
        return newChannel
    }

    private static func removeChannel(
        address: String,
        isStreamClientType: Bool
    ) -> ClientConnection? {
        let key = channelKey(address: address, isStreamClientType: isStreamClientType)
        stateLock.lock()
        let channel = channelByKey.removeValue(forKey: key)
        stateLock.unlock()
        return channel
    }

    private static func insertStreamState(_ streamState: StreamState) {
        stateLock.lock()
        streamById[streamState.streamId] = streamState
        stateLock.unlock()
    }

    private static func insertUnaryCall(
        _ call: UnaryCall<RawPayload, RawPayload>,
        requestId: String
    ) -> Bool {
        stateLock.lock()
        if cancelledUnaryCallIds.remove(requestId) != nil {
            stateLock.unlock()
            return false
        }
        unaryCallById[requestId] = call
        stateLock.unlock()
        return true
    }

    private static func removeUnaryCall(_ requestId: String) -> UnaryCall<RawPayload, RawPayload>? {
        stateLock.lock()
        let call = unaryCallById.removeValue(forKey: requestId)
        cancelledUnaryCallIds.remove(requestId)
        stateLock.unlock()
        return call
    }

    private static func cancelUnaryCall(_ requestId: String) -> UnaryCall<RawPayload, RawPayload>? {
        stateLock.lock()
        let call = unaryCallById.removeValue(forKey: requestId)
        if call == nil {
            cancelledUnaryCallIds.insert(requestId)
        }
        stateLock.unlock()
        return call
    }

    private static func getStreamState(_ streamId: String) -> StreamState? {
        stateLock.lock()
        let state = streamById[streamId]
        stateLock.unlock()
        return state
    }

    private static func removeStreamState(_ streamId: String) -> StreamState? {
        stateLock.lock()
        let state = streamById.removeValue(forKey: streamId)
        stateLock.unlock()
        return state
    }

    private static func hasEventLoopGroup() -> Bool {
        stateLock.lock()
        let isActive = eventLoopGroup != nil
        stateLock.unlock()
        return isActive
    }

    private static func getOrCreateEventLoopGroup() -> NIOTSEventLoopGroup {
        stateLock.lock()
        if let eventLoopGroup = eventLoopGroup {
            stateLock.unlock()
            return eventLoopGroup
        }

        let newEventLoopGroup = NIOTSEventLoopGroup(loopCount: 1)
        eventLoopGroup = newEventLoopGroup
        stateLock.unlock()
        return newEventLoopGroup
    }

    private static func cancelActiveUnaryCalls() {
        let unaryCalls: [UnaryCall<RawPayload, RawPayload>]

        stateLock.lock()
        unaryCalls = Array(unaryCallById.values)
        unaryCallById.removeAll()
        cancelledUnaryCallIds.removeAll()
        stateLock.unlock()

        for call in unaryCalls {
            call.cancel(promise: nil)
        }
    }

    private static func completeActiveStreams(statusMessage: String) {
        let streamStates: [StreamState]

        stateLock.lock()
        streamStates = Array(streamById.values)
        stateLock.unlock()

        for streamState in streamStates {
            streamState.cancelCall()
            streamState.pushComplete(
                .complete(
                    statusCode: 1,
                    statusMessage: statusMessage,
                    trailers: [:]
                )
            )
        }
    }

    private static func teardownResources() {
        let channels: [ClientConnection]
        let unaryCalls: [UnaryCall<RawPayload, RawPayload>]
        let groupToShutdown: NIOTSEventLoopGroup?

        stateLock.lock()
        channels = Array(channelByKey.values)
        channelByKey.removeAll()
        unaryCalls = Array(unaryCallById.values)
        unaryCallById.removeAll()
        cancelledUnaryCallIds.removeAll()
        streamById.removeAll()
        groupToShutdown = eventLoopGroup
        eventLoopGroup = nil
        stateLock.unlock()

        for call in unaryCalls {
            call.cancel(promise: nil)
        }

        for channel in channels {
            _ = try? channel.close().wait()
        }

        if let eventLoopGroup = groupToShutdown {
            try? eventLoopGroup.syncShutdownGracefully()
        }
    }

    private static func isValidMetadataKey(_ key: String) -> Bool {
        guard !key.isEmpty else {
            return false
        }
        return key.rangeOfCharacter(from: validMetadataKeyCharacters.inverted) == nil
    }

    private static func metadataFromDictionary(_ metadata: NSDictionary?) -> HPACKHeaders {
        var headers = HPACKHeaders()
        guard let metadata else {
            return headers
        }

        for (keyAny, valuesAny) in metadata {
            guard let key = keyAny as? String else {
                continue
            }
            let lowercaseKey = key.lowercased()
            guard isValidMetadataKey(lowercaseKey) else {
                continue
            }

            if let values = valuesAny as? [String] {
                for value in values {
                    headers.add(name: lowercaseKey, value: value)
                }
            } else if let values = valuesAny as? NSArray {
                for case let value as String in values {
                    headers.add(name: lowercaseKey, value: value)
                }
            }
        }

        return headers
    }

    private static func dictionaryFromMetadata(_ metadata: HPACKHeaders?) -> [String: [String]] {
        guard let metadata else {
            return [:]
        }

        var grouped: [String: [String]] = [:]
        for header in metadata {
            let lowercaseName = header.name.lowercased()
            guard isValidMetadataKey(lowercaseName) else {
                continue
            }
            grouped[lowercaseName, default: []].append(header.value)
        }
        return grouped
    }

    private static func decodeBase64(_ value: String) -> Data {
        Data(base64Encoded: value) ?? Data()
    }

    private static func requireString(_ params: NSDictionary, key: String) throws -> String {
        guard let value = params[key] as? String, !value.isEmpty else {
            throw NativeGrpcError.missingRequiredField(key)
        }
        return value
    }

    private static func grpcStatus(from error: Swift.Error) -> GRPCStatus {
        if let transformable = error as? GRPCStatusTransformable {
            return transformable.makeGRPCStatus()
        }
        return GRPCStatus(code: .unknown, message: String(describing: error))
    }

    private static func completeEvent(from error: Swift.Error, trailers: HPACKHeaders?) -> StreamEvent {
        let status = grpcStatus(from: error)
        return .complete(
            statusCode: status.code.rawValue,
            statusMessage: status.message ?? "",
            trailers: dictionaryFromMetadata(trailers)
        )
    }

    private static func asNSError(_ error: Swift.Error) -> NSError {
        return error as NSError
    }

    private static func errorMessage(for error: Swift.Error) -> String {
        let message = (error as NSError).localizedDescription
        if message.isEmpty {
            return String(describing: error)
        }
        return message
    }
}
