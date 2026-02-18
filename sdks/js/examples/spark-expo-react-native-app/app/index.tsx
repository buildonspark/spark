import { useRouter } from "expo-router";
import { useEffect, useState } from "react";
import {
  Button,
  ScrollView,
  StyleSheet,
  Text,
  TouchableOpacity,
  View,
} from "react-native";
import { useWallet } from "../contexts/WalletContext";

type MnemonicMode = "random" | "predefined";

const PREDEFINED_MNEMONIC =
  "soldier spare tell clog armed cup future grocery achieve duck butter awkward";

export default function HomeScreen() {
  const router = useRouter();
  const [mnemonicMode, setMnemonicMode] = useState<MnemonicMode>("random");
  const { wallet, isConnecting, error, connectWallet } = useWallet();

  useEffect(() => {
    if (wallet && !isConnecting) {
      router.replace("/wallet-details");
    }
  }, [wallet, isConnecting, router]);

  const handleConnectWallet = () => {
    if (mnemonicMode === "predefined") {
      connectWallet(PREDEFINED_MNEMONIC);
    } else {
      connectWallet();
    }
  };

  return (
    <ScrollView style={styles.container}>
      <View style={styles.content}>
        <Text style={styles.title}>Spark Wallet</Text>

        {error && (
          <View style={styles.errorContainer}>
            <Text style={styles.errorText}>Error: {error}</Text>
          </View>
        )}

        <View>
          <View style={styles.selectionContainer}>
            <Text style={styles.selectionLabel}>Mnemonic Type:</Text>
            <View style={styles.toggleContainer}>
              <TouchableOpacity
                style={[
                  styles.toggleButton,
                  mnemonicMode === "random" && styles.toggleButtonActive,
                ]}
                onPress={() => setMnemonicMode("random")}
              >
                <Text
                  style={[
                    styles.toggleButtonText,
                    mnemonicMode === "random" && styles.toggleButtonTextActive,
                  ]}
                >
                  Random
                </Text>
              </TouchableOpacity>
              <TouchableOpacity
                style={[
                  styles.toggleButton,
                  mnemonicMode === "predefined" && styles.toggleButtonActive,
                ]}
                onPress={() => setMnemonicMode("predefined")}
              >
                <Text
                  style={[
                    styles.toggleButtonText,
                    mnemonicMode === "predefined" &&
                      styles.toggleButtonTextActive,
                  ]}
                >
                  Predefined
                </Text>
              </TouchableOpacity>
            </View>
          </View>

          {mnemonicMode === "predefined" && (
            <View style={styles.infoBox}>
              <Text style={styles.infoBoxTitle}>Using Predefined Mnemonic</Text>
              <Text style={styles.infoBoxText} selectable>
                {PREDEFINED_MNEMONIC}
              </Text>
            </View>
          )}

          <View style={styles.buttonContainer}>
            <Button
              title={isConnecting ? "Connecting..." : "Connect Wallet"}
              onPress={handleConnectWallet}
              disabled={isConnecting}
            />
          </View>
        </View>
      </View>
      <View>
        <Button
          title="Open Test Screen"
          onPress={() => router.push("/test-screen")}
          testID="open-test-screen-button"
          color="#808080"
        />
      </View>
    </ScrollView>
  );
}

const styles = StyleSheet.create({
  container: {
    display: "flex",
    flex: 1,
    backgroundColor: "#f5f5f5",
  },
  content: {
    padding: 20,
  },
  title: {
    fontSize: 24,
    fontWeight: "bold",
    marginBottom: 20,
    textAlign: "center",
  },
  selectionContainer: {
    marginBottom: 20,
  },
  selectionLabel: {
    fontSize: 16,
    fontWeight: "600",
    marginBottom: 10,
    color: "#333",
  },
  toggleContainer: {
    flexDirection: "row",
    borderRadius: 8,
    overflow: "hidden",
    borderWidth: 1,
    borderColor: "#007aff",
  },
  toggleButton: {
    flex: 1,
    paddingVertical: 12,
    paddingHorizontal: 20,
    backgroundColor: "white",
    alignItems: "center",
    justifyContent: "center",
  },
  toggleButtonActive: {
    backgroundColor: "#007aff",
  },
  toggleButtonText: {
    fontSize: 16,
    color: "#007aff",
    fontWeight: "600",
  },
  toggleButtonTextActive: {
    color: "white",
  },
  infoBox: {
    backgroundColor: "#e8f4fd",
    padding: 15,
    borderRadius: 8,
    marginBottom: 15,
    borderWidth: 1,
    borderColor: "#007aff",
  },
  infoBoxTitle: {
    fontSize: 14,
    fontWeight: "600",
    color: "#007aff",
    marginBottom: 8,
  },
  infoBoxText: {
    fontSize: 12,
    color: "#333",
    fontFamily: "monospace",
  },
  buttonContainer: {
    marginTop: 10,
  },
  errorContainer: {
    backgroundColor: "#ffe5e5",
    padding: 15,
    borderRadius: 8,
    marginBottom: 15,
  },
  errorText: {
    color: "#ff3b30",
    fontSize: 14,
  },
});
