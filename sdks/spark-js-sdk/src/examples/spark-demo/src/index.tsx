import React from "react";
import ReactDOM from "react-dom/client";
import "./index.css";
import Root from "./Root";
import { SparkWalletProvider } from "./sparkwallet";
const root = ReactDOM.createRoot(
  document.getElementById("root") as HTMLElement
);
root.render(
  <React.StrictMode>
    <SparkWalletProvider>
      <Root />
    </SparkWalletProvider>
  </React.StrictMode>
);
