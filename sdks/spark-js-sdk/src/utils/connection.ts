import { createChannel, createClient } from "nice-grpc";
import {
  createChannel as createWebChannel,
  createClient as createWebClient,
} from "nice-grpc-web";
import { SparkServiceClient, SparkServiceDefinition } from "../proto/spark";
import { MockServiceClient, MockServiceDefinition } from "../proto/mock";

type SparkClient = SparkServiceClient & {
  close?: () => void;
};

type MockClient = MockServiceClient & {
  close: () => void;
};

export function createNewGrpcConnection(address: string): SparkClient {
  if (typeof window === "undefined") {
    // Node.js environment
    const channel = createChannel(address);
    const client = createClient(SparkServiceDefinition, channel);
    return { ...client, close: () => channel.close() };
  } else {
    // Browser environment
    // Channel connection is handled by the browser therefore we don't need to close it
    const channel = createWebChannel(address);
    return createWebClient(SparkServiceDefinition, channel);
  }
}

export function createMockGrpcConnection(address: string): MockClient {
  const channel = createChannel(address);
  return {
    ...createClient(MockServiceDefinition, channel),
    close: () => channel.close(),
  };
}
