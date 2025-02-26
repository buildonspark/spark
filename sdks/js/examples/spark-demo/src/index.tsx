import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import React from "react";
import ReactDOM from "react-dom/client";
import { SparkWalletProvider } from "./context/SparkWalletContext";
import "./index.css";
import Root from "./Root";

const queryClient = new QueryClient();

const root = ReactDOM.createRoot(
  document.getElementById("root") as HTMLElement,
);
root.render(
  <React.StrictMode>
    <SparkWalletProvider>
      <QueryClientProvider client={queryClient}>
        <Root />
      </QueryClientProvider>
    </SparkWalletProvider>
  </React.StrictMode>,
);
