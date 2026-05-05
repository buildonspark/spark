// Package mimo holds shared definitions and utilities for the MIMO
// (multi-input, multi-output) data model migration. It is the single
// source of truth for the things callers across handler/, dbseed, and
// future SSP-facing entry points need to agree on while the legacy
// column model and the edge-table model coexist.
//
// The package contains:
//
//   - Status sets (status.go) — pending and stuck per-table active-state
//     subsets used by partial indexes and raw-SQL filters. Anything that
//     reasons about "is this transfer/receiver still in flight?" reads
//     these.
//   - Compat helpers (compat.go) — read-from-edge-or-fall-back-to-column
//     accessors for migration-phase code paths gated by
//     KnobReadMIMODataModelTransferSend. These exist solely to bridge the
//     pre- and post-edge-table worlds and should be deletable once the
//     legacy columns are dropped.
//   - SQL fragment helpers (sql.go) — small, generic builders that emit
//     parameterized SQL clauses (network/types/transfer-id filters, time
//     bounds) for the raw-SQL pending/stuck-transfer queries. The query
//     builders themselves live in handler/ alongside their orchestration;
//     these helpers are pulled in from there.
//
// Out of scope: handler-side orchestration (auth, response shaping, error
// mapping), schema migrations, knob plumbing.
package mimo
