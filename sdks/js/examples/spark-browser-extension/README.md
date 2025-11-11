# Spark Browser Extension Example

Example browser extension demonstrating how to use the Spark SDK in both Chrome and Firefox extensions with background scripts and content scripts.

### Build for Both Browsers

```bash
yarn build
```

This creates two distributions:

- `dist/chrome/` - Chrome/Chromium extension
- `dist/firefox/` - Firefox extension

### Build for Specific Browser

```bash
yarn build:chrome   # Chrome only
yarn build:firefox  # Firefox only
```

### Development

```bash
yarn watch:all   # Auto-rebuild on changes
```

## 📦 Installation

### Chrome/Chromium

1. Open `chrome://extensions/`
2. Enable "Developer mode"
3. Click "Load unpacked"
4. Select the `dist/chrome/` folder

### Firefox

1. Open `about:debugging#/runtime/this-firefox`
2. Click "Load Temporary Add-on"
3. Select the `manifest.json` file in `dist/firefox/`

### WASM Loading

Browser extensions have special requirements for loading WebAssembly:

1. **Automatic Detection**: The SDK detects the extension environment
2. **Runtime API**: Uses browser extension APIs instead of `import.meta.url`
3. **CSP Compliance**: WASM files are loaded securely within the extension's CSP

## ⚙️ Configuration Requirements

Browser extensions using the Spark SDK need these configurations:

### 1. Manifest Configuration

WebAssembly is required for Spark SDK bindings. Per [MDN docs](https://developer.mozilla.org/en-US/docs/Mozilla/Add-ons/WebExtensions/Content_Security_Policy#webassembly) extensions must include `wasm-unsafe-eval` in their CSP to enable WebAssembly:

> Extensions wishing to use WebAssembly require 'wasm-unsafe-eval' to be specified in the script-src directive.
>
> From Firefox 102 and Chrome 103, 'wasm-unsafe-eval' can be included in the content_security_policy manifest.json key to enable the use of WebAssembly in extensions.
>
> Manifest V2 extensions in Firefox can use WebAssembly without 'wasm-unsafe-eval' in their CSP for backward compatibility. However, this behavior isn't guaranteed, see Firefox bug 1770909. Extensions using WebAssembly are therefore encouraged to declare 'wasm-unsafe-eval' in their CSP.

#### Content Security Policy

Update mainfest.json with the following:

```json
{
  "content_security_policy": {
    "extension_pages": "script-src 'self' 'wasm-unsafe-eval'; object-src 'self'"
  }
}
```

#### Content script support

Technically you can run Spark SDK directly in the content script of a Chrome extension, however in Firefox you'll encounter the following error while loading bindings `EvalError: call to Function() blocked by CSP`. This is due to Firefox applying CSP to content pages whereas Chrome does not. Instead, we recommend initializing Spark SDK in your extension background script and performing SDK operations via extension messaging as shown in this example in content.ts and background.ts.

## 📚 Browser Support

| Browser | Version | Notes                          |
| ------- | ------- | ------------------------------ |
| Chrome  | 109+    | Full Manifest V3 support       |
| Edge    | 109+    | Chromium-based, same as Chrome |
| Firefox | 109+    | Full Manifest V3 support       |
| Brave   | 109+    | Chromium-based, same as Chrome |
