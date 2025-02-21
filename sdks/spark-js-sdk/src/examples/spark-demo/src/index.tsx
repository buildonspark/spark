import React from "react";
import ReactDOM from "react-dom/client";
import "./index.css";
import Root from "./Root";
import { SparkWalletProvider } from "./context/SparkWalletContext";
import { BtcPriceProvider } from "./context/BtcPriceContext";
const root = ReactDOM.createRoot(
  document.getElementById("root") as HTMLElement
);
root.render(
  <React.StrictMode>
    <BtcPriceProvider>
      <SparkWalletProvider>
        <Root />
      </SparkWalletProvider>
    </BtcPriceProvider>
  </React.StrictMode>
);
