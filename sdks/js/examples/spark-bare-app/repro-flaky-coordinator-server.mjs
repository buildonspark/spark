import http from "node:http";
import { QueryNodesResponse } from "@buildonspark/spark-sdk/proto/spark";

const port = Number(process.argv[2] || "45454");

function encodeGrpcWebFrame(payload, isTrailer = false) {
  const frame = Buffer.alloc(5);
  frame[0] = isTrailer ? 0x80 : 0x00;
  frame.writeUInt32BE(payload.length, 1);
  return Buffer.concat([frame, Buffer.from(payload)]);
}

function buildGrpcWebUnaryResponse(message) {
  const trailer = Buffer.from("grpc-status: 0\r\n\r\n", "utf8");
  return Buffer.concat([
    encodeGrpcWebFrame(message),
    encodeGrpcWebFrame(trailer, true),
  ]);
}

let requestCount = 0;
const sockets = new Set();

const server = http.createServer((req, res) => {
  requestCount += 1;
  console.log(`request #${requestCount} ${req.method} ${req.url}`);

  if (requestCount === 1) {
    console.log("intentionally hanging first unary request");
    return;
  }

  const responseBytes = QueryNodesResponse.encode({
    nodes: {},
    offset: -1,
  }).finish();
  const body = buildGrpcWebUnaryResponse(responseBytes);

  res.writeHead(200, {
    "content-type": "application/grpc-web+proto",
    "content-length": body.length,
    date: new Date().toUTCString(),
    "x-processing-time-ms": "1",
  });
  res.end(body);
});

server.on("connection", (socket) => {
  sockets.add(socket);
  socket.on("close", () => sockets.delete(socket));
});

server.listen(port, "127.0.0.1", () => {
  console.log(`flaky coordinator listening on http://127.0.0.1:${port}`);
});

function shutdown() {
  for (const socket of sockets) {
    socket.destroy();
  }
  server.close(() => process.exit(0));
}

process.on("SIGINT", shutdown);
process.on("SIGTERM", shutdown);
