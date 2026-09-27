# Plugin protocols

Plugins are zip packages containing `plugin.yaml`, `contributions.json`, and
one executable per supported `GOOS-GOARCH`. The host starts the selected
executable as a separate process. Native gRPC is the recommended production
transport; HTTP and stdio remain available for compatibility and simple tools.
TCP ports are always allocated by the host, so manifest TCP addresses are
ignored.

## Manifest

HTTP and gRPC use `tcp` or `unix`; stdio must use `stdio`:

```yaml
id: admin.example-plugin
name: Example Plugin
version: 1.0.0
engine:
  type: process
  protocol: grpc # http | grpc | stdio
  transport:
    windows-amd64: tcp
    linux-amd64: unix
  socket:
    linux-amd64: runtime/plugin.sock
  entry:
    windows-amd64: bin/windows-amd64/plugin.exe
    linux-amd64: bin/linux-amd64/plugin
compatibility:
  app: ">=1.0.0, <2.0.0"
  api: v1
extension_points:
  - admin.routes
  - public.routes
  - user.routes
permissions:
  - route:register
  - route:public:register
  - route:user:register
```

For stdio, set each platform transport to `stdio` and omit address/socket.

Plugin API v1 supports three route extension points. Each requires its matching
permission and a typed entry in `contributions.json`:

| Extension point | Required permission |
| --- | --- |
| `admin.routes` | `route:register` |
| `public.routes` | `route:public:register` |
| `user.routes` | `route:user:register` |
Other extension-point names are rejected rather than silently registered.
Unknown and duplicate permissions/extension points are also rejected. A
contribution using all three audiences is:

```json
{
  "admin.routes": [{"method": "POST", "path": "/admin/auth/items/*"}],
  "public.routes": [{"method": "GET", "path": "/public/feed"}],
  "user.routes": [{"method": "GET", "path": "/user/auth/profile"}]
}
```

The three host routes use the same plugin ID namespace and forward the
corresponding path to the plugin process:

| Host URL | Authentication | Contribution point |
| --- | --- | --- |
| `/api/v1/plugin/{id}/public/*` | None | `public.routes` |
| `/api/v1/plugin/{id}/user/auth/*` | User access JWT and active account | `user.routes` |
| `/api/v1/plugin/{id}/admin/auth/*` | Admin access JWT and Casbin permission | `admin.routes` |

Each URL also works without the final `/*` segment. Existing admin plugin URLs
outside these prefixes keep their admin JWT and Casbin checks. The catch-all
admin route cannot enter the reserved public or user namespaces.
Public route contributions must begin with `/public`, and user route
contributions must begin with `/user/auth`. A declaration in one audience never
authorizes a request in another. A plugin only exposing admin routes remains
inaccessible to guests and ordinary users.

The host removes client `Authorization`, cookies, and `X-Go-Admin-Plugin-User-ID`
before proxying. For an authenticated user route it sets
`X-Go-Admin-Plugin-User-ID` to the verified user UID; on public routes this
header is absent. The plugin must use the host supplied identity instead of
trusting identity values sent by a client. The header is also filtered from
plugin responses.

Only contributed method/path pairs are accepted by the host proxy. `*` is
allowed as a route method and a trailing `/*` is allowed as a path wildcard.
Path traversal, query fragments, backslashes, unknown fields, undeclared
contribution types, and empty declared contribution lists are rejected.
The host also evaluates `compatibility.app` as a semantic-version constraint
against `plugin.HostVersion` (default `1.0.0`). Release builds must set the
actual application version with `-ldflags "-X go-admin/internal/plugin.HostVersion=<version>"`.

## Plugin entry point

The public `go-admin/pluginapi` package provides the common request/response
contract and authenticated servers:

```go
handler := func(ctx context.Context, req *pluginapi.ProxyRequest) (*pluginapi.ProxyResponse, error) {
    return &pluginapi.ProxyResponse{StatusCode: 200, Body: []byte("ok")}, nil
}

switch os.Getenv(pluginapi.EnvProtocol) {
case "http":
    log.Fatal(pluginapi.ServeHTTP(http.HandlerFunc(myHTTPHandler)))
case "grpc":
    log.Fatal(pluginapi.ServeGRPC(handler))
case "stdio":
    log.Fatal(pluginapi.ServeStdio(context.Background(), handler))
}
```

HTTP and gRPC plugins may log to stdout/stderr. Stdio reserves stdout for
length-prefixed protobuf frames, so stdio plugins must log only to stderr.
The host supplies `PLUGIN_ID`, `PLUGIN_VERSION`, `PLUGIN_API_VERSION`,
`PLUGIN_PROTOCOL`, `PLUGIN_TRANSPORT`, `PLUGIN_ADDRESS`/`PLUGIN_SOCKET`, and a
cryptographically random per-start `PLUGIN_TOKEN`. Do not log the token.
TCP endpoint discovery files and Unix sockets are host-owned and created below
the configured plugin temporary directory, never in the immutable version
directory. The manifest socket value is a portable declaration; the host
supplies the actual absolute socket path through `PLUGIN_SOCKET`.

All modes expose an authenticated lifecycle control plane:

- `GET /_go-admin/handshake` returns API, plugin ID, and plugin version
  identity. Startup fails closed if any value differs from the manifest.
- `GET /health` reports readiness.
- `POST /_go-admin/shutdown` drains the server before process termination.

The public proxy blocks the `/_go-admin` namespace and strips lifecycle/token
headers in both directions. gRPC and stdio carry the same control paths inside
their protobuf request envelope.

Host calls have a 30-second deadline. Startup/readiness has a 5-second limit;
shutdown gets 2 seconds to drain and is then force-killed. An enabled plugin
that exits unexpectedly is marked unavailable and restarted at most three
times with bounded exponential backoff. Manual disable cancels further
recovery.

Plugin list/detail responses expose `status`, `error_message`, `restarting`,
`installed_at`, `enabled_at`, and `updated_at`. Treat `status=error` as an
operational alert; `restarting=true` means bounded automatic recovery is still
in progress.

## Package trust and upgrades

Every installation snapshots the package before validation and writes
`.go-admin-integrity.json` into the installed version. It contains the package
SHA-256, signer fingerprint, and SHA-256 for every extracted file. The complete
set is checked before every start or restart; changing or deleting a packaged
file prevents execution.

Production must enable detached Ed25519 signatures:

```yaml
plugin:
  require_signature: true
  trusted_public_keys: "<base64-32-byte-public-key>[,<next-key>]"
```

Environment equivalents are `CMF_PLUGIN_REQUIRE_SIGNATURE` and
`CMF_PLUGIN_TRUSTED_PUBLIC_KEYS`. Put the Base64 signature in
`<package>.zip.sig`. The signed message is the raw 32-byte SHA-256 digest of the
ZIP, not its hexadecimal text. Multiple public keys support key rotation.
When `CMF_APP_ENV=production`, startup fails unless signature enforcement is
enabled and at least one trusted public key is configured.

Installing a different version of an existing plugin performs an upgrade (or
downgrade). For an enabled plugin the host stops the old process, records the
new version as non-routable, starts and health-checks it, then publishes it as
enabled. If any start, handshake, health, or database step fails, the host
restores the previous manifest, directory, registry entries, process, and
enabled state. Reinstalling a previous signed package uses the same rollback
path. Old version directories are removed together when the plugin is
uninstalled.

## Security and limits

- Installation accepts only regular zip files inside `data/plugin-packages`;
  symlink escapes, unsafe versions, archive symlinks, more than 2,048 files,
  packages over 128 MiB, and expanded content over 512 MiB are rejected.
- Linux plugin packages must preserve the executable bit on their selected
  entry. ZIP extraction preserves the archived Unix file mode.
- Requests and responses are limited to 16 MiB. Sensitive request/response
  headers and the runtime token are never exposed through the public proxy.
- Runtime processes inherit only a small allowlist of OS environment variables;
  application/database secrets are not inherited.
- Plugin binaries are native code running as the application OS user. Only
  trusted packages should be installed; the process boundary prevents a
  plugin panic from crashing the host but is not an OS security sandbox.

For production, run the application itself under an OS service account with
least privilege and enforce CPU, memory, process-count, filesystem, and network
limits using the deployment boundary (Windows Job Object/container policy,
systemd/cgroup, or Kubernetes). The portable Go host intentionally does not
pretend that application-level permission declarations sandbox native code.

API v1's lifecycle coordinator and install paths are host-local. The supported
deployment topology is one application instance per plugin store. A
multi-replica deployment must drain and roll out each replica with its own
signed package/store; do not point replicas on different hosts at one shared
plugin database and assume this API provides distributed upgrade coordination.

## Production checklist

- Require signatures and keep private signing keys outside the deployment host.
- Prefer gRPC over loopback TCP or a Unix socket; use stdio only when sequential
  request processing is acceptable.
- Store mutable plugin data outside the version directory; packaged files are
  immutable and integrity checked.
- Alert on plugin status `error`, exhausted restart attempts, signature errors,
  integrity mismatches, and failed rollback.
- Keep plugin packages and their `.sig` files until the release rollback window
  closes.
- Exercise upgrade, downgrade, crash, hung request, and shutdown paths in a
  staging environment built for the production `GOOS-GOARCH`.
