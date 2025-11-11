async function main() {
  console.log("[spark-extension] Content script loaded");

  const container = document.createElement("div");
  container.id = "spark-extension-demo";
  container.style.position = "fixed";
  container.style.bottom = "16px";
  container.style.right = "16px";
  container.style.background = "#fff";
  container.style.color = "#000";
  container.style.padding = "8px 12px";
  container.style.border = "1px solid #ccc";
  container.style.zIndex = "2147483647";
  container.innerHTML = `
    <div style="font-family: sans-serif;">
      <strong>Spark Extension</strong><br/>
      <span id="spark-status">Requesting wallet info...</span>
    </div>
  `;

  document.body.appendChild(container);

  const statusEl = container.querySelector("#spark-status");

  chrome.runtime.sendMessage({ type: "GET_WALLET_ADDRESS" }, (response) => {
    console.log("[spark-extension] content received response", response);
    if (response?.address) {
      statusEl.textContent = `Address: ${response.address}`;
    } else if (response?.error) {
      statusEl.textContent = `Error: ${response.error}`;
    } else {
      statusEl.textContent = `Wallet: ${response?.walletState || "unknown"}`;
    }
  });
}

main();
