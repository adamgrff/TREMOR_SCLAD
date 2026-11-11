# DB-04 / Э1Б-И3 — API protection

Production admission disabled. Foundation is not a production warehouse write API.

## Exact route inventory

Extracted from the actual apiRoutes registration executed by its route-coverage test, including expanded loops. 51 registered method/path pairs: 22 GET (17 admitted and 5 blocked), 29 writes (all blocked). S/I/U/D/X denote SELECT/INSERT/UPDATE/DELETE/EXECUTE. Existing legacy write/function handlers remain available only through explicit legacy-development mode. Protected SELECT columns are enumerated by security_api_read_v1.sql; no table-wide or future-object SELECT/default grant is added.

| Method | Endpoint | Handler | SQL / side effects in original handler | Required legacy/read privileges | Protected decision |
|---|---|---|---|---|---|
| GET | /health | healthHandler | No SQL; liveness only | none | Liveness only, no DB |
| GET | /api/deployment | deploymentIdentityHandler | Read-only tx: deployment_identity | S deployment_identity(singleton,deployment_id) | Fresh verifier → original read handler |
| GET | /api/assembly/orders | assemblyOrdersHandler | Tx: INSERT draft ON CONFLICT; FOR UPDATE; assembly_load_order + stock reads; rolled back on return | I draft; S/U lock rights; X assembly_load_order | 501: INSERT/row lock in GET |
| GET | /api/assembly/mapping-products | assemblyMappingProductsHandler | S non-archived products; no user SQL function | S products(id,sku,name,archived) | Fresh verifier → original read handler |
| POST | /api/assembly/problems/{problemID}/map-product | assemblyMappingHandler → saveAssemblyWorkspace | Shared tx: mapping confirmation, affected positions/workspace/receipts | DML assembly domain; X legacy functions | 501: protected command not implemented |
| POST | /api/assembly/disassembly/{sessionID}/start | disassemblyHandler → saveAssemblyWorkspace | Shared tx; action start: disassembly/stock/workspace/events/receipts | DML assembly domain; X legacy functions | 501: protected command not implemented |
| POST | /api/assembly/disassembly/{sessionID}/scan | disassemblyHandler → saveAssemblyWorkspace | Shared tx; action scan: disassembly/stock/workspace/events/receipts | DML assembly domain; X legacy functions | 501: protected command not implemented |
| POST | /api/assembly/disassembly/{sessionID}/finish | disassemblyHandler → saveAssemblyWorkspace | Shared tx; action finish: disassembly/stock/workspace/events/receipts | DML assembly domain; X legacy functions | 501: protected command not implemented |
| POST | /api/assembly/disassembly/{sessionID}/pause | disassemblyHandler → saveAssemblyWorkspace | Shared tx; action pause: disassembly/stock/workspace/events/receipts | DML assembly domain; X legacy functions | 501: protected command not implemented |
| POST | /api/assembly/disassembly/{sessionID}/undo-return | disassemblyHandler → saveAssemblyWorkspace | Shared tx; action undo-return: disassembly/stock/workspace/events/receipts | DML assembly domain; X legacy functions | 501: protected command not implemented |
| GET | /api/ozon/status | ozonConnectionHandler | Local configuration presence only; no outbound HTTP | none | Fresh verifier → original read handler |
| POST | /api/assembly/start | warehouseAssemblyHandler → saveAssemblyWorkspace | Shared tx; action start: stock/reservation/order/workspace/events/receipts; finish may invoke Ozon workflow | DML assembly domain; X legacy functions | 501: protected command not implemented |
| POST | /api/assembly/scan | warehouseAssemblyHandler → saveAssemblyWorkspace | Shared tx; action scan: stock/reservation/order/workspace/events/receipts; finish may invoke Ozon workflow | DML assembly domain; X legacy functions | 501: protected command not implemented |
| POST | /api/assembly/undo | warehouseAssemblyHandler → saveAssemblyWorkspace | Shared tx; action undo: stock/reservation/order/workspace/events/receipts; finish may invoke Ozon workflow | DML assembly domain; X legacy functions | 501: protected command not implemented |
| POST | /api/assembly/finish | warehouseAssemblyHandler → saveAssemblyWorkspace | Shared tx; action finish: stock/reservation/order/workspace/events/receipts; finish may invoke Ozon workflow | DML assembly domain; X legacy functions | 501: protected command not implemented |
| GET | /api/assembly-preview/draft | getAssemblyDraftHandler | S draft + assembly_load_workspace | S draft; X assembly_load_workspace | 501: requires forbidden user-function EXECUTE |
| GET | /api/assembly/problems | assemblyProblemsHandler | S aggregate + assembly_load_problem | S problems/orders; X assembly_load_problem | 501: requires forbidden user-function EXECUTE |
| GET | /api/assembly/problems/archive | assemblyProblemArchiveHandler | RR read-only tx: filtered count + summaries via direct joins; no user function | S explicit problems,orders,positions,events,affected columns | Fresh verifier → original read handler |
| GET | /api/assembly/problems/{problemID}/history | assemblyProblemHistoryHandler | RR read-only tx: assembly_load_problem/order; event/journal enrichment | S assembly journals; X load functions | 501: requires forbidden user-function EXECUTE |
| POST | /api/assembly/problems | createAssemblyProblemHandler → saveAssemblyWorkspace | Shared tx: workspace/version guards, problem and event/receipt creation | DML assembly domain; X legacy functions | 501: protected command not implemented |
| POST | /api/assembly/problems/{problemID}/check-stock | assemblyProblemOperationHandler → saveAssemblyWorkspace | Shared tx; action check-stock: stock/problem/journal/receipt changes; Ozon actions may invoke configured external client | DML assembly domain; X legacy functions | 501: protected command not implemented |
| POST | /api/assembly/problems/{problemID}/return | assemblyProblemOperationHandler → saveAssemblyWorkspace | Shared tx; action return: stock/problem/journal/receipt changes; Ozon actions may invoke configured external client | DML assembly domain; X legacy functions | 501: protected command not implemented |
| POST | /api/assembly/problems/{problemID}/retry-ozon | assemblyProblemOperationHandler → saveAssemblyWorkspace | Shared tx; action retry-ozon: stock/problem/journal/receipt changes; Ozon actions may invoke configured external client | DML assembly domain; X legacy functions | 501: protected command not implemented |
| POST | /api/assembly/problems/{problemID}/close-cancelled | assemblyProblemOperationHandler → saveAssemblyWorkspace | Shared tx; action close-cancelled: stock/problem/journal/receipt changes; Ozon actions may invoke configured external client | DML assembly domain; X legacy functions | 501: protected command not implemented |
| POST | /api/assembly/problems/{problemID}/transfer-disassembly | assemblyProblemOperationHandler → saveAssemblyWorkspace | Shared tx; action transfer-disassembly: stock/problem/journal/receipt changes; Ozon actions may invoke configured external client | DML assembly domain; X legacy functions | 501: protected command not implemented |
| GET | /api/assembly/problems/{problemID} | assemblyProblemHandler | S assembly_load_problem(id) | S problems; X assembly_load_problem | 501: requires forbidden user-function EXECUTE |
| PUT | /api/assembly-preview/draft | saveAssemblyDraftHandler → saveAssemblyWorkspace | Shared tx: workspace/version locks, business commands, operation receipts | DML assembly domain; X legacy functions | 501: protected command not implemented |
| POST | /api/racks | createRackHandler | Tx: create category and cells | I categories,cells; sequence | 501: protected command not implemented |
| GET | /api/racks | racksHandler | S categories + cells; ordered rack rows | S categories,cells columns | Fresh verifier → original read handler |
| GET | /api/home/history | homeHistoryHandler | S union: receiving intake/placements; assembly lifecycle/problems; Ozon observations | S explicit event + referenced columns | Fresh verifier → original read handler |
| GET | /api/home/statistics | homeStatisticsHandler | DB clock + RR read-only tx: receiving, stock, assembly aggregate queries | S explicit statistics columns | Fresh verifier → original read handler |
| GET | /api/receiving/pending-summary | receivingPendingSummaryHandler | S pending item sum joined to active sessions | S items(session_id,quantity),sessions(id,status) | Fresh verifier → original read handler |
| POST | /api/receiving/sessions/{sessionID}/quantities/{productID} | receivingQuantityHandler | Tx: deployment/receipt/idempotency guards, item delta and intake events | S/I/U/D receiving; receipts; sequences | 501: protected command not implemented |
| PUT | /api/receiving/sessions/{sessionID}/quantities/{productID} | receivingQuantityHandler | Tx: deployment/receipt/idempotency guards, item delta and intake events | S/I/U/D receiving; receipts; sequences | 501: protected command not implemented |
| POST | /api/product-catalog | saveCatalogProductHandler | Tx: receipt/deployment guard; product/catalog writes; optional receiving intake | S/I/U products,receiving,receipts; sequences | 501: protected command not implemented |
| PUT | /api/product-catalog/{productID} | saveCatalogProductHandler | Tx: receipt/deployment guard; product/catalog writes; optional receiving intake | S/I/U products,receiving,receipts; sequences | 501: protected command not implemented |
| GET | /api/product-catalog | productCatalogHandler | RR read-only tx: identity, categories, products, stock aggregation | S identity,categories,products,cell_stock columns | Fresh verifier → original read handler |
| GET | /api/cells/{cellName} | cellHandler | Two S: existence then products/cell_stock join | S cells,products,cell_stock columns | Fresh verifier → original read handler |
| GET | /api/products/{sku} | productHandler | S products by SKU, archived filter | S products(id,name,sku,archived) | Fresh verifier → original read handler |
| POST | /api/receiving/placements | createReceivingPlacementHandler | Tx: guards, pending to placement/stock, receipt | S/I/U/D receiving + stock + receipts; sequences | 501: protected command not implemented |
| POST | /api/receiving/placements/{placementID}/undo | undoReceivingPlacementHandler | Tx: guards, undo placement/stock, restore pending, receipt | S/I/U/D receiving + stock + receipts | 501: protected command not implemented |
| POST | /api/receiving/finish | finishReceivingHandler | Tx: guards, placement checks, complete session, receipt | S/I/U receiving + receipts | 501: protected command not implemented |
| GET | /api/receiving/sessions/active | activeReceivingSessionsHandler | One S with pending/placement subqueries | S sessions,items,placements columns | Fresh verifier → original read handler |
| GET | /api/receiving/sessions/latest | latestReceivingHandler | S latest completed session with placement/cell/product joins | S sessions,placements,placement_items,cells,products columns | Fresh verifier → original read handler |
| GET | /api/receiving/sessions | completedReceivingHistoryHandler | RR read-only tx: identity and paginated summaries | S identity,sessions,placements,placement_items columns | Fresh verifier → original read handler |
| POST | /api/receiving/sessions | createReceivingSessionHandler | Tx: deployment/receipt guard, INSERT session, receipt | S/I sessions + receipts; sequence | 501: protected command not implemented |
| GET | /api/receiving/sessions/{sessionID} | getReceivingSessionHandler | RR read-only tx: identity, session, pending and placements | S identity,sessions,items,placements,placement_items,cells,products columns | Fresh verifier → original read handler |
| DELETE | /api/receiving/sessions/{sessionID} | deleteReceivingHistoryHandler | Tx: deployment/receipt guard, soft deletion, receipt | S/I/U sessions,receipts | 501: protected command not implemented |
| POST | /api/receiving/sessions/{sessionID}/items | addReceivingSessionItemHandler | Tx: guards, product lookup; item/intake/receipt writes | S/I/U receiving,products,receipts; sequences | 501: protected command not implemented |
| DELETE | /api/receiving/sessions/{sessionID}/items/{sku} | deleteReceivingSessionItemHandler | Tx: deployment/revision guards, DELETE item, revision/receipt writes | S/I/U/D items,sessions,receipts | 501: protected command not implemented |
| GET | /api/products/{sku}/stock | productStockHandler | Two S: product then cell_stock/cells/warehouses | S products,cell_stock,cells,warehouses columns | Fresh verifier → original read handler |

HEAD follows each GET policy, including denied GETs. Unknown paths/methods retain ServeMux 404/405; canonical redirects re-enter the same policy. OPTIONS is handled by existing CORS middleware without SQL or business execution. No other router or administrative deployment/apply endpoint is registered by cmd/api. Future unlisted routes default to 501 in protected mode. The existing /api/deployment only returns deployment identity after readiness; it is not itself a readiness proof.

## Mode, credentials and HTTP contract

Startup requires process environment `TREMOR_API_MODE=protected` or
`TREMOR_API_MODE=legacy-development`; an unset/unknown value terminates startup.
Only explicit legacy development loads .env or DATABASE_URL, and only a loopback
PostgreSQL endpoint is accepted. No protected error re-enters that branch.
Existing handlers and business logic were moved into routes.go unchanged.

Protected mode reads `TREMOR_RUNTIME_CONFIG`, with exactly these fields:

```json
{
  "target": {"cluster":"<approved TEST system id>","database":"tremor_e1b_collector_test","deployment":"<approved TEST deployment UUID>","schema":"public"},
  "database_oid": 12345,
  "password_file":"/runtime/password",
  "ca_file":"/runtime/ca.pem",
  "cert_file":"/runtime/cert.pem",
  "key_file":"/runtime/key.pem",
  "verifier_url":"https://127.0.0.1:8443/internal/readiness"
}
```

This is a private deployment configuration, not caller HTTP input or a new
approval mechanism. Password and private key are separate untracked secret files.
The API's PostgreSQL host/port/user are fixed at 127.0.0.1:5432/tremor_api; no
administrative URL, username override or fallback is used. The adapter refuses
any database other than the fixed disposable database, any non-public schema,
and missing database OID. Thus production admission stays explicitly disabled.
The independent verifier proves cluster/deployment/frontier and current approved
security state; RuntimePool also checks actual current_user/database/OID.

RuntimePool now uses `pg_catalog,<quoted verifier-bound schema>`: original read
handlers use unqualified SQL table names. Its old pg_catalog-only path could not
resolve those tables. The schema is the fixed trusted target, quoted with
pgx.Identifier; API has no CREATE there. This changes no collector or legacy
readiness/deparser context. READ ONLY is supplementary to real PostgreSQL ACL.

| Situation | HTTP | Stable code / behavior |
|---|---|---|
| Write command without protected implementation | 501 | protected_operation_unsupported; handler not entered, no transaction/SQL |
| Blocked GET requiring legacy writes/functions | 501 | protected_operation_unsupported |
| NOT_READY, timeout, mTLS error, wrong binding, missing verifier | 503 | protected_not_ready; admitted read handler not entered |
| Invalid parameters on an admitted read | Existing 400 | Original endpoint validation / response preserved |
| Missing resource | Existing 404 | Original response preserved |
| Unknown URL / wrong HTTP method | 404 / 405 | ServeMux; no business handler |
| /health | 200 | Liveness only; does not claim DB readiness |

Each admitted business/service read uses readiness.Require with fresh nonce and
mTLS verdict before handler entry. Pool connection/acquisition adds another
fresh check. Concurrent saturation may fail with 503, never legacy fallback.
Verdict and subsequent SQL are separate transactions; no absolute race-free
claim against trusted administrators is made. ACL independently reject business
DML, DDL, ledger reads/writes, owner SET ROLE and user-function EXECUTE.

## Versioned column policy / protected deployment

`backend/internal/migrations/security_api_read_v1.sql` is the complete explicit
column allowlist, embedded as inert APIReadPolicyV1 release SQL. It grants USAGE
on public and SELECT only on the listed columns of 20 named tables (count the
SQL relation list, including categories/warehouses in one statement). No ledger,
transition control table, sequence, function EXECUTE, default privilege or future
object/table-wide SELECT grant is added.

No API startup code executes this SQL. The TEST fixture installs it only when
I3_READ_POLICY=yes, as part of its separately approved first protected extension
(version 25 in the synthetic fixture). It uses the existing ApplyProtectedExtension:
manifest/prefix, source target binding, independent pre-apply rehearsal/checkpoint
approval, advisory lock, metadata/readiness postflight and one COMMIT with receipt.
The policy SQL is canonicalized with the unchanged CanonicalSQL before the TEST
manifest checksum is established. No baseline/old manifest entry is rewritten;
old no-policy I2.2/F1 fixtures run separately and retain their original behavior.
No post-apply observed hash becomes an approval. Production loaders remain empty.

Migration catalog scope already captures each column ACL; the new TEST final
checkpoint independently approves those exact deltas. foundationSecurity() and
all existing deployment/collector/recovery algorithms are untouched.

## Legacy SQL dependency closure

The abbreviated 'assembly domain' in the table denotes the shared transaction in
saveAssemblyWorkspace (assembly_preview.go), not a read grant proposal. It locks
assembly_preview_drafts/version, uses assembly_preview_requests for idempotency,
and dispatches action-specific logic in assembly_warehouse.go,
assembly_problem_stock.go, assembly_mapping.go, assembly_disassembly.go and
assembly_ozon_journal.go. Depending on action, its SQL reaches products/cells/stock,
assembly_order_records/versions/positions/heads, assembly_order_reservations, assembly_cell_references,
assembly_pick_records, assembly_preview_stock_holds, assembly_warehouse_operations,
assembly_stock_checks, assembly_sku_mappings, assembly_problem_records,
assembly_problem_affected_positions/events/contexts/clearances,
assembly_disassembly_sessions/items/returns/return_reversals and the
assembly_disassembly_effective_returns view, lifecycle events and
assembly_ozon_operations/observations/problem_links/reconciliations. The legacy
load/store functions include assembly_load_order/problem/workspace,
assembly_store_order/workspace and assembly_resolve_product. All paths remain blocked before dispatch in protected
mode; action-specific adapters and their own reviewed privileges are deferred.

Receiving mutations use beginReceivingOperation/requireReceivingDeployment and
receiving_operation_requests; scan/quantity/catalog flows additionally use their
existing request ledgers. Multi-query stock/pending/session/placement/event effects
and receipt handling remain in the original handlers, never partially executed
in protected mode. New command adapters are not implemented here.

## Frontend

No design changes. Existing successful GET DTOs remain unchanged. Receiving
501/503 errors preserve the durable command/UUID and surface a protected-mode
message through ReceivingProtectedBlocked; assembly preserves pending intent and
local state and displays an error. Neither path signals success, clears operation
data or schedules endless retries. Existing catalog/history/quantity UI already
updates local state only after successful server responses. New regression tests
exercise the real receiving journal and assembly hook, including manual retry of
the identical UUID and absence of automatic retries. Browser visual QA is
NOT_TESTED; frontend automated checks and build are separate evidence below.

## Harness

Use the existing run-readiness.ps1 with a freshly compiled Linux migrations test
binary and Linux cmd/api test binary. Add `-ApiTestBinary <api.test>` for all 51
real HTTP routes; add `-PopulateTestData` for the synthetic stock/history/receipts
upgrade scenario. Without ApiTestBinary it remains the original F1 regression.
The extra compose.api.yaml mounts only runtime secrets into API and no verifier
password/policy/deployer secret. The TEST controller alone can mutate fixtures,
then checks actual API HTTP 503 and restores them. It also compares full collector
evidence for business data, identity, historic receipts, sequence state and ledger
OID/rows/metadata against pre-request evidence; this evidence is not an approval.

The API HTTP TEST process uses the same openRuntime and apiRoutes as cmd/api and
real PostgreSQL/mTLS, not a mocked verdict. TEST-only /internal/test-ready exists
only in that test executable, never in cmd/api routes. Wrong target, concurrent
GETs, HEAD/canonical path variants and all registered writes are covered. A query
tracer asserts zero SQL for blocked route calls. Test-only admission artifacts
remain isolated from production executable/release approval.

Legacy regressions use compose.api-legacy.yaml over the existing collector harness
with the API test binary. Only the TEST administrator receives TEST_DATABASE_URL;
all old receiving/assembly tests create/drop their own synthetic schemas. The API
readiness containers never receive those administrator credentials.

References checked: [Go ServeMux](https://pkg.go.dev/net/http#ServeMux) and
[PostgreSQL 18 GRANT](https://www.postgresql.org/docs/18/sql-grant.html).

## Acceptance / limitations (2026-11-09)

| Class | Result | Evidence |
|---|---|---|
| All Go unit/offline packages, including retained migrations/readiness tests | PASS | go test ./... -count=1 |
| Existing API PostgreSQL + explicit legacy HTTP | PASS | 129 top-level PASS; TEST-only protected service skipped here and covered separately |
| Existing collector regressions | PASS | all 47 subtests; no old check weakened |
| I2.2 fresh/upgrade, rollback/recovery, protected apply/repeat | PASS | existing execution fixture repeated in isolated fresh/upgrade setup |
| I3-F1 without new API grants | PASS | independent original no-policy project |
| All 51 actual protected HTTP route pairs, fresh and populated target | PASS | 17 reads retain JSON/404/400 contracts; 29 writes and 5 GETs return 501; tracer blocked_SQL=0 |
| Business data/identity/history receipts/sequences/ledger preservation | PASS | complete collector evidence before HTTP vs after HTTP and after restored fault tests |
| Wrong target and concurrent HTTP GETs | PASS | wrong binding rejects before handler SQL; concurrent requests return only 200 or fail-closed 503 |
| Existing API under NOT_READY/drift/missing receipt/wrong anchor | PASS | separate TEST controller mutates state, actual /api/racks returns 503, restores state and gets 200 |
| Stopped verifier | PASS | actual registered /api/racks returns 503, in addition to retained F1 adapter test |
| Runtime SQL role / no ledger / DML/DDL/SET ROLE/EXECUTE denials | PASS | actual tremor_api and tremor_migrator PostgreSQL connections |
| Frontend automated checks and build | PASS | all 25 scripts, including new real-hook/journal protected-response test; npm run build |
| Formatting / vet / whitespace / secret review | PASS | gofmt, go vet ./..., git diff --check; no passwords/keys in Git |
| Browser visual/manual warehouse workflow QA | NOT_TESTED | no design change; write workflows intentionally unavailable |
| Production-approved API/verifier deployment | NOT_TESTED / DISABLED | no production approval, provisioning or working-DB connection |

Final pinned TEST image:
`tremor-e1a-test@sha256:475363c2a602b7b96dd7568ff86d45bdd703721f323e10ccac6bffe9f06e5fe2`,
PostgreSQL 18.6. Final projects (all cleanup complete, zero containers/networks/volumes):

- Fresh HTTP: `e1b-readiness-60503b74d109411ea8624c21386736d2`, system id `7706166048589258795`.
- Populated HTTP: `e1b-readiness-04190966128243cb9b8e30ed375715d0`, system id `7706166346081927211`.
- Collector: `e1b-collector-0d99929fba8146ce95270310494f1a4d`.
- Original F1: `e1b-readiness-7291113547e64fad86a6630a764ace78`, system id `7706164580479533099`.
- Legacy API: `e1b-api-legacy-d575957bc1d049c880c17d56b08455d2`.

Development failures are not counted as PASS: an initial embedded SQL CRLF hash
mismatch was rejected by the existing loader and fixed by unchanged CanonicalSQL;
an empty fixture initially assumed a product existed. The fixtures now test both
empty/missing-resource contracts and positive populated reads. Both failed Docker
projects were cleaned up. PowerShell harness syntax/argument issues were corrected
before final complete runs. No guard was relaxed.

Changed components: cmd/api/main.go, routes.go, protected.go and their tests;
internal/readiness/runtime.go; inert versioned API read policy and TEST protected
fixture/export/controller; compose overlays and existing readiness runner;
receivingScanCommands.ts, useAssemblyDraft.ts and one frontend regression script;
this inventory/report. Business handlers, warehouse algorithms, E1A, baseline and
manifests remain unchanged. main stays at 63efe4bddc3aa8c7303b1bfa4f7276f3191f00f7
(tree 53d259004b87eef5ed6a39cf544385ca635be23d). Seven main-worktree user files retain
the SHA256 values recorded in DB04_E1B_READINESS_VERIFIER.md; they were rechecked.

Remaining boundaries: no protected business command exists, five legacy/function
GETs remain closed, production admission is disabled, and fresh readiness cannot
eliminate the administrator race between verifier and API transactions. The code
is ready for the next separately authorized foundation stage, not production
warehouse operation. No I4 work is included.

## Changed files

| File | Purpose |
|---|---|
| backend/cmd/api/main.go | Explicit mode and runtime initialization; original routes delegated unchanged |
| backend/cmd/api/routes.go | One registry of all 51 existing routes |
| backend/cmd/api/protected.go | Explicit read policy, pre-handler refusals and runtime-only connection configuration |
| backend/cmd/api/protected_test.go | Exhaustive route refusal coverage, mode and error contract checks |
| backend/cmd/api/protected_integration_test.go | Real HTTP/SQL/mTLS acceptance, target/concurrency/legacy scenarios |
| backend/internal/readiness/runtime.go | Quoted verifier-bound application schema in safe runtime search_path |
| backend/internal/migrations/security_api_read_v1.sql | Versioned inert 20-table column SELECT allowlist |
| backend/internal/migrations/security_api_read_policy.go | Embed the release SQL without automatic application |
| backend/internal/migrations/security_protected_apply_integration_test.go | Optional independently approved TEST read-policy migration and canonical bytes |
| backend/internal/migrations/security_verifier_integration_test.go | Runtime TEST config, real HTTP fault checks and before/after integrity evidence |
| backend/test/e1b/compose.readiness.yaml | TEST policy selector and controller-only private evidence mount |
| backend/test/e1b/compose.api.yaml | Separate restricted API HTTP TEST process |
| backend/test/e1b/compose.api-legacy.yaml | Existing API PostgreSQL regressions on isolated synthetic schemas |
| backend/test/e1b/run-readiness.ps1 | Extend existing harness with HTTP binary and populated-target options |
| frontend/src/pages/ReceivingPage/receivingScanCommands.ts | Protected refusal messages with durable command preservation |
| frontend/src/pages/AssemblyPage/useAssemblyDraft.ts | Protected refusal messages without clearing local intent/state |
| frontend/scripts/test-protected-api.mjs | Real frontend journal/hook 501/503 regression |
| docs/database/DB04_E1B_API_PROTECTION.md | Complete endpoint inventory, protocol, privileges and evidence |
