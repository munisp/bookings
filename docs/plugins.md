# Plugin tools (SPEC-W3 §4, innovation 15)

## MVP (this wave): declarative HTTP tools

Industry packs declare `customTools` entries:

```yaml
customTools:
- name: check_calendar_availability
  description: Check a consultant's open calendar slots for a time range.
  method: GET                # GET|POST|PUT|PATCH|DELETE
  url: http://booking:7002/public/sites/{{site_slug}}/availability
  bodyTemplate: '{"day": "{{day}}"}'   # optional, non-GET methods
```

Semantics:

- **Validation (fail-fast)**: identity-service's pack loader validates name
  (function-tool-safe, unique per pack), method allowlist and absolute
  http(s) URL; invalid packs are rejected at load time.
- **Registration**: the voice runtime adds each custom tool to the LLM tool
  list next to the built-in receptionist tools. The parameter schema is
  derived from `{{var}}` placeholders in `url`/`bodyTemplate` (session
  context vars `site_slug`, `tenant_slug`, `tenant_id` are pre-bound and not
  exposed to the model).
- **Execution**: `{{var}}` substitution from the tool-call arguments, then an
  httpx request. GET/DELETE send leftover args as query params; other methods
  send the rendered `bodyTemplate` (or raw args JSON). Responses return
  `{status, http_status, body}`; HTTP >= 400 and network errors surface as
  tool errors to the model.
- **SSRF guard**: the URL hostname must match the `PLUGIN_ALLOWED_HOSTS`
  allowlist (exact or subdomain), default `booking,knowledge,identity` —
  pack tools can reach platform services only, never arbitrary hosts. The
  check runs at registration and again per call (after substitution).
- Example: `industries/consultancy.yaml` → `check_calendar_availability`.

## Phase 2 (design): WASM-sandboxed plugins

HTTP tools cover request/response integrations but cannot run custom logic
safely. Phase 2 replaces the executor with a WASM sandbox:

1. Packs reference a `.wasm` module (OCI artifact or mounted volume) with an
   declared capabilities manifest (allowed hosts, max fuel, memory cap).
2. The runtime instantiates the module per tool call (wasmtime-py) with:
   - WASI disabled except a host-provided `http_request` import that enforces
     the same `PLUGIN_ALLOWED_HOSTS` allowlist host-side;
   - fuel/epoch limits (CPU) and a memory ceiling; per-call timeout;
   - input = JSON args on stdin, output = JSON result on stdout.
3. Signing: modules signed (sigstore) and verified at pack load; unsigned
   modules only run in a `PLUGIN_UNSAFE_DEV=1` dev mode.
4. The declarative HTTP form remains as sugar that compiles to a built-in
   generic HTTP WASM module, keeping pack YAML backwards-compatible.

Non-goals for phase 2: persistent plugin state (use platform services via the
allowlisted HTTP import), outbound network beyond the allowlist, host FS.
