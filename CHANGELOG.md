## [v1.0.0-alpha.15] — 2026-02-27

### Changed
- **Kernel split S1 (approval/policy first extraction)**
  - Added `internal/controlplane/core/approvalpolicy` service as the first reusable core boundary for approval + policy orchestration.
  - Moved approval submission/wait orchestration out of server route/task closures into the new core service (behavior preserved).
  - Moved policy-apply orchestration (template lookup, fleet policy update, push fallback) behind the same core service.
- **Kernel split S4 (approval decision side-effect hooks)**
  - Moved approval decision audit/event sequencing into `internal/controlplane/core/approvalpolicy` hooks so routes no longer own ordering.
  - Wired default hooks to existing approval-decided + approved-dispatch audit/event emissions with no API response changes.
- **Kernel split S5 (approval decide error contract extraction)**
  - Added shared core→API decide-error mapping helper in `internal/controlplane/core/approvalpolicy` and refactored `handleDecideApproval` to decode → core call → contract-driven encode.
  - Preserved decide API outcomes: denied stays 200 with request payload; approved dispatch failures stay 502 with unchanged wording; invalid decision/request errors stay `400 invalid_request`.
- **Kernel split S6 (approval decide success contract extraction)**
  - Extracted decide success decode/encode contracts into `internal/controlplane/core/approvalpolicy` and reduced `handleDecideApproval` to contract decode → core call → contract encode.
  - Added success parity assertions for denied/approved `{status,request}` responses to lock response schema/fields.
- **Kernel split S7 (approval decide transport adapter contract)**
  - Added a unified decide transport adapter contract in internal/controlplane/core/approvalpolicy (single success/error envelope, HTTP/MCP-ready) and refactored handleDecideApproval to consume adapter output with near-zero branching.
  - Added transport-contract parity coverage across decode, success, invalid-decision, dispatch-failure, and hook-failure decide scenarios.
- **Kernel split S8 (approval decide renderer/projection split)**
  - Extracted approval decide response projection + HTTP renderer abstraction so handleDecideApproval only orchestrates decode/core decision while transport writing is contract-driven.
  - Added renderer parity tests in approvalpolicy/server to lock unchanged decide status codes, error wording, and {status,request} response fields for future MCP projection reuse.
- **Kernel split S9 (approval decide MCP renderer parity)**
  - Added MCP approval-decide renderer wired to the shared `approvalpolicy` decide transport projection so HTTP and MCP consume one core projection contract path.
  - Added MCP parity tests for `{status,request}` success payloads and approved-dispatch failure wording without changing existing HTTP/MCP behavior.
- **Kernel split S10 (approval decide orchestration seam)**
  - Extracted shared decide orchestration helper (`decode -> decide -> project -> render-target selection`) in `internal/controlplane/core/approvalpolicy` and refactored HTTP decide route to consume the new helper directly.
  - Added MCP orchestration seam wiring (stub path for future decide tool exposure) plus cross-transport parity tests to lock unchanged decide status/error payload behavior.
- **Kernel split S11 (approval decide MCP tool exposure)**
  - Exposed `legator_decide_approval` MCP tool using the shared decide orchestration seam and renderer contracts, with parity tests to lock HTTP+MCP equivalent success/error behavior.
- **Kernel split S20 (shared transport writer kernel)**
  - Extracted a shared HTTP/MCP writer kernel and routed approval + command response codecs through normalized response envelopes into this kernel while preserving existing response bodies/status/messages.
  - Added strict legacy-vs-kernel parity coverage for command and approval flows across both HTTP and MCP render paths.
- **Kernel split S21 (unified response-envelope builders)**
  - Added a shared response-envelope builder interface in `internal/controlplane/core/transportwriter` and implemented builder adapters for approval + command flows before writer-kernel dispatch.
  - Preserved existing HTTP/MCP status codes, payload shapes, and error messages; added builder-level parity tests for approval + command transports.
- **Kernel split S22 (shared surface→transport resolver seam)**
  - Extracted a shared surface-to-transport resolver seam in `internal/controlplane/core/transportwriter` and wired both approval + command response flows through it, removing duplicated per-domain mapping helpers.
  - Added cross-domain parity tests to lock resolver behavior and unsupported-surface fallback precedence (HTTP callback first, MCP fallback second) without changing external HTTP/MCP responses.
- **Kernel split S23 (shared unsupported-surface fallback helper)**
  - Extracted a shared unsupported-surface fallback helper in `internal/controlplane/core/transportwriter` and reused it in approval + command dispatch adapters while preserving existing HTTP-first/MCP-second behavior.
  - Added parity coverage to lock unsupported-surface fallback precedence plus exact status/code/message semantics across approval and command paths.
- **Kernel split S24 (shared unsupported-surface envelope factory)**
  - Extracted a shared unsupported-surface envelope factory in `internal/controlplane/core/transportwriter` and reused it across approval + command codecs/adapters to remove duplicate fallback envelope construction.
  - Added strict parity tests locking exact unsupported-surface HTTP status/code/message and MCP error text semantics across approval + command fallback paths.
- **Kernel split S25 (shared unsupported-surface message formatter seam)**
  - Extracted `transportwriter.UnsupportedSurfaceMessage(scope, surface)` and routed approval + command unsupported-surface codecs/adapters through it, removing duplicated message string construction.
  - Added formatter parity coverage to lock exact unsupported-surface message text while preserving existing HTTP/MCP fallback semantics.
- **Kernel split S26 (typed unsupported-surface scopes + parity locks)**
  - Added typed unsupported-surface scope constants in `transportwriter` and routed approval + command unsupported-surface message generation through typed scopes (raw scope literals removed from core call sites).
  - Added strict parity coverage to lock exact scope literal values, rendered unsupported-surface message strings, and HTTP-first/MCP-fallback behavior semantics.
- **Kernel split S27 (domain scope wrappers for unsupported-surface messages)**
  - Added tiny approval/command domain wrapper helpers for unsupported-surface scope constants so core call sites no longer reference `transportwriter` scope constants directly.
  - Preserved exact unsupported-surface message text and HTTP-first/MCP-fallback behavior, with focused parity tests for approval + command wrappers.
- **Kernel split S28 (domain envelope wrappers for unsupported-surface fallbacks)**
  - Added tiny approval/command domain wrapper helpers for unsupported-surface envelope construction and routed approval/command codecs + adapters through those wrappers instead of direct `transportwriter.UnsupportedSurfaceEnvelope(...)` calls.
  - Added strict parity coverage to lock exact unsupported-surface envelope semantics (`500 internal_error` + unchanged MCP error text) and HTTP-first/MCP-fallback behavior.
- **Kernel split S29 (domain fallback dispatch wrappers)**
  - Added tiny domain-level unsupported-surface fallback dispatch wrappers for approval + command flows and routed remaining adapter fallback wiring through these wrappers.
  - Added strict fallback-dispatch parity tests to lock HTTP-first then MCP fallback precedence with unchanged `status/code/message` semantics.
- **Kernel split S30 (shared HTTP-error contract adapter)**
  - Added shared `transportwriter` helper(s) to convert `transportwriter.HTTPError` into domain `HTTPErrorContract` values and writer callbacks.
  - Reused the shared adapter in approval + command wrappers, preserving exact `status/code/message` conversion and HTTP-first/MCP-fallback behavior with strict legacy-parity tests.
- **Kernel split S31 (shared success-payload adapter helper)**
  - Added shared `transportwriter` success-payload conversion helpers (type assertion + optional normalization) and reused them in approval/command wrappers.
  - Preserved exact success payload semantics (including nil normalization and HTTP-first/MCP-fallback behavior) with focused legacy-parity tests for approval + command conversion callbacks.
- **Kernel split S32 (domain success-writer wrappers)**
  - Added tiny approval/command success-writer constructor wrappers around the shared `transportwriter` success adapter and routed remaining domain call sites through those wrappers.
  - Added strict wrapper-vs-legacy parity tests to lock type assertion semantics, nil normalization behavior, success payload shape, and HTTP-first/MCP fallback semantics.
- **Kernel split S33 (domain writer-kernel constructors)**
  - Added tiny approval + command domain constructors that assemble full `transportwriter.WriterKernel` callbacks (HTTP error/success + MCP error/success) and replaced inline per-field wiring call sites.
  - Added strict constructor-vs-legacy parity tests to lock error/success payload mapping, nil handling, and HTTP-first/MCP fallback behavior.
- **Kernel split S34 (unsupported-surface fallback writer constructors)**
  - Added tiny approval + command domain constructors for unsupported-surface fallback writer callbacks and replaced remaining inline fallback writer wiring call sites.
  - Added strict constructor-vs-legacy parity tests to lock HTTP-first/MCP-fallback precedence and exact unsupported-surface HTTP + MCP error semantics.
- **Kernel split S35 (shared unsupported-surface fallback constructor adapter)**
  - Added `transportwriter.AdaptUnsupportedSurfaceFallbackWriter(...)` to centralize unsupported-surface fallback writer assembly from domain HTTP-adapter + MCP passthrough callbacks.
  - Kept approval + command fallback constructors as thin wrappers over the shared helper and added strict helper-vs-legacy parity tests to lock HTTP-first/MCP-fallback precedence plus exact HTTP status/code/message and MCP text semantics.
- **Kernel split S36 (shared unsupported-surface fallback dispatch invocation helper)**
  - Added `transportwriter.DispatchUnsupportedSurfaceFallback(...)` to centralize unsupported-surface fallback invocation (`envelope + writer construction + dispatch`) while keeping approval + command domain policy seams explicit.
  - Replaced duplicated approval/command fallback call-shapes with the shared helper and added strict parity tests against the legacy repeated invocation path to preserve HTTP-first/MCP-fallback precedence and exact HTTP + MCP error text semantics.
- **Kernel split S37 (shared unsupported-surface scope-envelope builder seam)**
  - Added `transportwriter.UnsupportedSurfaceEnvelopeBuilderForScope(...)` as a tiny shared seam for scope-to-envelope wiring and routed approval/command unsupported-surface envelope wrappers through it while keeping domain scope ownership intact.
  - Added strict parity coverage that compares seam-built envelopes against the legacy `UnsupportedSurfaceMessage(scope, surface) -> UnsupportedSurfaceEnvelope(...)` wiring path to preserve exact message/envelope semantics and fallback precedence behavior.
- **Kernel split S38 (typed unsupported-surface adapter seam)**
  - Added tiny shared typed-surface adapters (`BuildUnsupportedSurfaceEnvelope(...)` and `UnsupportedSurfaceMessageForSurface(...)`) so `string`, `ProjectionDispatchSurface`, `DecideApprovalRenderSurface`, and transport surfaces all reuse the same scope-bound unsupported-surface call path.
  - Removed remaining direct `string(surface)` casts from approval/command unsupported-surface production paths while preserving exact message/envelope text and HTTP-first/MCP-fallback semantics, with strict typed-seam-vs-legacy-cast parity tests.
- **Kernel split S39 (projection adapter unsupported-surface helper seam)**
  - Added tiny shared projection-adapter helper for unsupported-surface fallback dispatch plus optional handled-flag wiring, and routed approval/command projection adapters through it without changing domain-owned fallback/envelope construction.
  - Added strict adapter-level parity tests against pre-helper inline branches to lock unchanged HTTP-first/MCP-fallback behavior and command handled-flag outcomes.
- **Kernel split S40 (resolve-or-unsupported dispatch seam)**
  - Added a tiny shared resolve-or-unsupported branch seam for projection adapters and reused it in approval + command dispatch/read + command invoke paths while keeping domain resolvers, policy registries, and fallback builders local.
  - Added strict parity tests against pre-seam inline branches to lock resolve-vs-unsupported branching, HTTP-first/MCP-fallback outcomes, and command handled-flag behavior.
- **Kernel split S12 (approval decide invoke adapter parity)**
  - Extracted a shared decide invoke adapter for approval_id/body assembly and invoke-closure wiring, then refactored HTTP and MCP decide entrypoints to consume it with behavior preserved.
- **Kernel split S13 (approval decide render-target registry boundary)**
  - Added a shared render-target registry boundary so HTTP and MCP decide surfaces resolve their renderer target via registry selection while preserving existing success/error contracts.
- **Kernel split S14 (surface-neutral decide response dispatch adapter)**
  - Added a shared decide response dispatch adapter in internal/controlplane/core/approvalpolicy so HTTP/MCP shells now provide only transport writers while centralized surface policy selects success/error emission behavior.
  - Added dispatch-adapter parity tests to lock unchanged HTTP/MCP decide outputs, status/error mapping, and error wording.
- **Kernel split S15 (projection dispatch policy registry generalization)**
  - Generalized projection dispatch policy registry/adapter selection into reusable `internal/controlplane/core/projectiondispatch` utilities and applied them to approval-decide render-target + dispatch policy selection with parity tests.
  - Added non-invasive command-dispatch/read surface registry hooks for upcoming extractions without changing current command behavior.
- **Kernel split S16 (command dispatch/read projection adapters)**
  - Applied the shared `core/projectiondispatch` registry utility to command dispatch-error and command-read projection paths, introducing centralized HTTP/MCP adapter selection without external response changes.
  - Added strict HTTP/MCP command parity tests that compare legacy vs adapter outputs/errors for dispatch and read flows.
- **Kernel split S17 (shared command invoke seam for HTTP+MCP)**
  - Added shared command invoke adapter in `internal/controlplane/core/commanddispatch` to centralize request-id generation, wait/stream/timeout policy selection, and renderer handoff projection.
  - Refactored HTTP command dispatch route + MCP `legator_run_command` tool to consume the shared invoke seam with strict parity tests preserving existing response/error semantics.
- **Kernel split S18 (command invoke render-dispatch adapter)**
  - Added a shared command invoke render-dispatch adapter in `internal/controlplane/core/commanddispatch` so HTTP/MCP shells now provide transport writers only while core owns sequencing + fallback policy.
  - Refactored HTTP command dispatch and MCP run-command renderers to consume the adapter with strict parity tests preserving existing JSON/tool error semantics.
- **Kernel split S19 (command transport response codecs)**
  - Added shared core response codecs for command invoke HTTP JSON response/error payloads and MCP text/error payloads in `internal/controlplane/core/commanddispatch`.
  - Refactored HTTP command dispatch and MCP run-command renderers into pure transport wiring over codec outputs, with strict legacy-vs-codec parity tests to preserve status, payload shape, and error wording semantics.

### Added
- Parity tests for the extracted core service and server policy-apply paths (not found + offline apply-local behavior).
- **Kernel split S3 (dispatch contract + policy envelope)**
  - Added a unified core command-dispatch result envelope with shared API/MCP error mapping helpers.
  - Introduced `DispatchPolicy` options (wait/stream/timeout/cancel semantics) and migrated API, MCP, and server dispatch callers.
  - Preserved external command responses while reducing duplicated mapping logic across API and MCP surfaces.

## [v1.0.0-alpha.14] — 2026-02-27

### Added
- **Network Device Probes MVP (phase 1)**
  - SQLite-backed `network_devices` target store (id, name, host, port, vendor, username, auth mode, tags, timestamps)
  - Auth-protected API endpoints:
    - `GET /api/v1/network/devices`
    - `POST /api/v1/network/devices`
    - `GET /api/v1/network/devices/{id}`
    - `PUT /api/v1/network/devices/{id}`
    - `DELETE /api/v1/network/devices/{id}`
    - `POST /api/v1/network/devices/{id}/test` (safe connectivity check)
    - `POST /api/v1/network/devices/{id}/inventory` (best-effort hostname/version/interfaces)
  - Permission model wired to RBAC: read routes require `fleet:read`; mutating/test/inventory routes require `fleet:write`
  - New **Network Devices** page under the existing template system (no CDN dependencies), including list/add/edit/delete plus test/inventory actions with write-permission gating
  - Unit tests for network device store + handlers, plus server permission coverage for all network-device routes
  - E2E checks expanded for network-device CRUD and probe/inventory endpoint behavior

## [v1.0.0-alpha.13] — 2026-02-27

### Changed
- **RBAC parity hardening across API routes**
  - Discovery scan + install-token endpoints now require `fleet:write`
  - Model Dock create/update/delete/activate endpoints now require `fleet:write`
  - Cloud Connector create/update/delete/scan endpoints now require `fleet:write`
- **Page-level permission alignment**
  - `/approvals` now requires `approval:read`
  - `/audit` now requires `audit:read`
- **UI permission gating**
  - Sidebar navigation now hides links the current role cannot access
  - Write actions on Approvals/Alerts/Model Dock/Cloud Connectors/Discovery are read-only or disabled when `fleet:write` / `approval:write` is missing

### Added
- **Authorization denial audit events**
  - New `auth.authorization_denied` audit event recorded for permission denials
  - Captures method/path/required permission/reason without request payload leakage
- **RBAC regression tests** for denied mutation paths, page-scope checks, template permission helpers, and denial audit emission

## [v1.0.0-alpha.12] — 2026-02-27

### Added
- **MCP tool surface** via official Go MCP SDK (`github.com/modelcontextprotocol/go-sdk v1.3.1`)
  - SSE transport endpoint at `GET /mcp`
  - 7 tools: `legator_list_probes`, `legator_probe_info`, `legator_run_command`, `legator_get_inventory`, `legator_fleet_query`, `legator_search_audit`, `legator_probe_health`
  - 2 resources: `legator://fleet/summary`, `legator://fleet/inventory`
- **MCP E2E coverage** — endpoint reachability check and version regression check

### Changed
- **Registration tokens** now support `no_expiry=true` for persistent multi-use tokens (DaemonSet-safe)

### Fixed
- **Discovery E2E safety** — replaced `192.168.1.0/24` scan with loopback-only `127.0.0.0/24` + timeout to prevent outbound net-scan alerts

### Stats
- **~160 Go files** | **30+ test suites** | **49/49 E2E passing**

## [v1.0.0-alpha.11] — 2026-02-27

### Added
- **Audit log JSONL export** — `GET /api/v1/audit/export` streams full audit log as newline-delimited JSON with filter support (`probe_id`, `type`, `since`, `until`)
- **Audit log CSV export** — `GET /api/v1/audit/export/csv` streams audit events as CSV with 6 key columns
- **Cursor pagination** on `GET /api/v1/audit` — `limit`, `cursor` parameters, response includes `next_cursor` and `has_more`
- **Audit retention auto-purge** — configurable via `audit_retention` in config or `LEGATOR_AUDIT_RETENTION` env var (e.g. `30d`, `90d`)
- **Manual audit purge** — `DELETE /api/v1/audit/purge?older_than=30d` (admin-only)
- **Landing page** — sparse prose-first design at `/site/`, public (no auth), dark theme, ASCII architecture diagram, system font stack
- **E2E test expansion** — model dock, cloud connectors, discovery APIs, audit export/CSV/purge (42 → 45 tests)

### Fixed
- **Probe WebSocket reconnection** — exponential backoff now resets after successful connection; `Connected()` flag properly cleared on disconnect
- **DaemonSet control-plane coverage** — added NoSchedule/NoExecute tolerations, removed node selector that excluded control plane nodes
- **Landing page auth skip** — `/site/*` added to auth middleware skip paths

### Changed
- **Documentation refresh** — README and getting-started guide updated for alpha.10 features, config table, API sections, architecture diagram

## [v1.0.0-alpha.10] — 2026-02-27

### Added
- **K8s DaemonSet probe deployment** — container image, DaemonSet manifests, multi-use registration tokens, auto-init from environment variables, K8s inventory enrichment (cluster, node, namespace, pod metadata)
- **Windows probe MVP** — cross-compilation, Windows service management, platform-specific inventory, command execution
- **BYOK model dock** — user-provided API key profiles per vendor, runtime model switching, usage tracking UI
- **Cloud connectors MVP** — inventory APIs and adapters for external cloud accounts, dedicated UI page
- **Auto-discovery + registration assist MVP** — network/SSH probe scanning, registration assist with generated install commands, discovery UI page
- **UI overhaul** — shared `_base.html` layout architecture, warm dark palette, design tokens, zero CDN dependencies, inline SVG icons, system font stack, consolidated JS (`app.js` with `LegatorUI` namespace)
- **Fleet page redesign** — three-panel master-detail layout (tree navigator + probe detail + activity feed), resizable split panes, status grouping (Online/Degraded/Offline/Pending), tag filtering, hostname search, 5 detail tabs (System/Network/Services/Packages/Chat)
- **Embedded probe chat** — Chat tab in fleet detail panel with WebSocket connection, message history, typing indicator, auto-scroll
- **Clear chat endpoint** — `DELETE /api/v1/probes/{id}/chat` with UI button
- Sidebar navigation consistency across all template pages
- Per-page template loading (`map[string]*template.Template`)
- `BasePage` struct with `CurrentUser`, `Version`, `ActiveNav`

### Fixed
- Alerts engine race condition (nil channel deref on Stop/loop race)
- DaemonSet security context for Kyverno + PodSecurity compliance
- Registration tags sent in initial request (eliminated separate API call)
- Container image Dockerfile podman compatibility
- SSH template placeholder quote escaping in discovery UI

### Stats
- **155 Go files** | **30 test suites** | **5 probes online** (2 bare metal + 3 K8s DaemonSet)
- Control plane: **14MB** | Probe: **7.1MB** | legatorctl: **5.7MB**


# Changelog

All notable changes to Legator are documented here.

## [v1.0.0-alpha.6] — 2026-02-26

### Added
- OIDC authentication (optional SSO via Keycloak, Auth0, Okta, Google, etc.)
- Consistent JSON error responses with error codes across all API endpoints
- Graceful LLM-down handling (user-friendly message instead of 500)
- WebSocket resilience (malformed JSON survived, connections not dropped)
- Dark UI with centered chat layout (Claude Desktop-inspired)
- Warm colour palette and typography polish
- Chat slide-over context panel (hidden by default)
- Textarea input with auto-resize, Enter to send
- Empty state with "Ask this probe anything" prompt
- Configuration reference documentation
- Security model documentation
- This changelog

### Fixed
- Chat history race condition (history skipped when WebSocket connected first)
- WebSocket "Disconnected" indicator showing before connection attempted
- Context panel excessive vertical space
- Responsive breakpoint too aggressive (changed from 1180px to 900px)
- Tailwind CDN removed from production (replaced with hand-written utility classes)

## [v1.0.0-alpha.5] — 2026-02-26

### Added
- Token lifecycle hardening with list tokens API
- Command classifier with defence-in-depth policy enforcement
- Install script hardening with SHA256 verification
- Request-derived install commands in registration response
- Policy persistence across probe restarts
- README rewrite, getting-started guide, and architecture documentation

## [v1.0.0-alpha.4] — 2026-02-26

### Added
- Multi-user RBAC (admin, operator, viewer roles)
- Login UI with session-based authentication
- User management API (create, list, delete)
- Probe WebSocket authentication (API key verification)
- Multi-user RBAC design document

## [v1.0.0-alpha.3] — 2026-02-26

### Added
- Build/version injection hardening with Makefile
- Webhook delivery metrics and diagnostics endpoint
- Incremental SSE updates on probe detail page
- Deployment and upgrade guide

## [v1.0.0-alpha.2] — 2026-02-26

### Added
- Real-time SSE updates on probe detail page
- Webhook notifier (wired to event bus)
- Scoped API key permissions on all routes
- Server package unit tests (31 tests)
- Chat page with probe context panel
- Probe delete and fleet cleanup endpoints
- WebSocket keepalive and LLM chat integration

## [v1.0.0-alpha.1] — 2026-02-26

### Added
- Standalone Go control plane (no Kubernetes dependency)
- Probe agent (systemd service, WebSocket connection, heartbeat)
- Fleet management (register, list, health scoring, tags)
- Command dispatch with HMAC-SHA256 signing
- Output streaming (SSE)
- LLM-powered chat per probe
- Policy engine (observe/diagnose/remediate)
- Approval queue with risk classification
- Audit log (SQLite, immutable)
- Web UI (fleet dashboard, probe detail, chat)
- REST API (35+ endpoints)
- Prometheus metrics
- Event bus (pub/sub)
- CI/CD (test, build, lint, e2e, multi-arch release)
- Install script for one-liner probe deployment
