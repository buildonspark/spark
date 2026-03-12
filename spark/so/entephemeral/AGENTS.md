# entephemeral Notes

- Purpose: store only data that must never be recoverable from backups.
- Keep this database minimal; default new data to the main DB unless there is a strict forget requirement.
- Cross-database operations are best-effort (no distributed transaction). Preserve explicit failure handling and divergence logging.
- Link records to main DB via stable IDs/version fields; do not rely on cross-database foreign keys.
- If schema/semantics change, update `README.md` in this directory in the same PR.

