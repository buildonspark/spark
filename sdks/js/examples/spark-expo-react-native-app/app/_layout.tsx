import "@azure/core-asynciterator-polyfill";
import "react-native-get-random-values";
import "text-encoding";

import { Stack } from "expo-router";
import { WalletProvider } from "../contexts/WalletContext";

export default function RootLayout() {
  return (
    <WalletProvider>
      <Stack>
        <Stack.Screen name="index" options={{ title: "Connect Wallet" }} />
        <Stack.Screen name="wallet-details" options={{ title: "My Wallet" }} />
        <Stack.Screen name="test-screen" options={{ title: "Test Screen" }} />
      </Stack>
    </WalletProvider>
  );
}
