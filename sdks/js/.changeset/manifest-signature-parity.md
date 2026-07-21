---
"@buildonspark/spark-sdk": minor
"@buildonspark/cli": patch
---

Add signTransferManifest/verifyTransferManifestSignature — the sender's identity-key ECDSA signature over manifest_hash (the transfer-package signing scheme applied to the manifest digest). Signatures are verify-checked, not byte-pinned (ECDSA is nonce-dependent); only the manifest hash stays byte-identical across languages. The CLI gains dev-only signmanifest/verifymanifest commands for cross-checking.
