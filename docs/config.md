# xbind configuration

Every setting the daemon reads, in one place. This page is generated from
the daemon's configuration struct (`internal/boot.Config`; `make check` fails
when it is stale), so it is complete by construction.

**Precedence:** a command-line flag beats its environment variable, which
beats the default. A flag's default *is* the environment variable's value
when one is set (`xbind -h` shows the resolved defaults). Boolean flags have
no environment form. Settings marked *read by* are consulted by that
package where they are used, not at boot; they are listed here so nothing
is undocumented.

```
xbind init <dir>     scaffold a workspace (also happens automatically on an
                     empty --workspace)
xbind [flags]        serve a workspace
xbind version
```

| Setting | Flag | Environment | Default | Read by | What it does |
|---|---|---|---|---|---|
| Workspace | `--workspace` | `XBIN_WORKSPACE` | `/workspace` | boot | workspace directory |
| Listen | `--listen` | `XBIN_LISTEN` | `127.0.0.1:8642` | boot | listen address |
| Dev | `--dev` |  | off | boot | dev mode: web/docs served from source tree, debug logs |
| DevOverlay | `--dev-overlay` | `XBIN_DEV_OVERLAY` |  | boot | dev only (needs --dev): a directory whose files shadow the workspace's on the /c/ static plane — `make dev` points it at workspace-template/ so the shell and admin tile are served from source without copying them into the workspace; manifests are never overlaid |
| NoAuth | `--no-auth` |  | off | boot | disable auth (dev only; every request is admin) |
| ScopeUIDs | `--scope-uids` |  | off | boot | run each scope's backends under a dedicated uid (requires root; auth tier 2) |
| InsecureVault | `--insecure-vault` |  | off | boot | store secrets AND resource data as PLAINTEXT at rest (not recommended; --no-auth implies it; a bare --dev instead auto-encrypts with a dev key) |
| Isolate | `--isolate` |  | off | boot | run each backend in a per-component sandbox (namespaces + overlay rootfs; auth tier 3, needs --rootfs) |
| Rootfs | `--rootfs` | `XBIN_ROOTFS` |  | boot | base rootfs dir (unpacked OCI image) for --isolate sandboxes |
| IngressListen | `--ingress-listen` | `XBIN_INGRESS_LISTEN` |  | boot | public ingress HTTP listener (docs/ingress.md; "" = off). Serves ONLY published tile routes — never the console |
| IngressCert | `--ingress-cert` | `XBIN_INGRESS_CERT` |  | boot | TLS certificate (PEM) for the ingress listener (with --ingress-key; reloaded on change) |
| IngressKey | `--ingress-key` | `XBIN_INGRESS_KEY` |  | boot | TLS key (PEM) for the ingress listener |
| TrustedProxy | `--trusted-proxies` | `XBIN_TRUSTED_PROXIES` |  | boot | comma-separated IPs/CIDRs of trusted reverse proxies whose X-Forwarded-For is honored (login throttle, session IP attribution, /c/ warm-IP gate). Default: trust nobody. REQUIRED when xbind sits behind a proxy, else all clients key on the proxy's IP |
| ExternalURL | `--external-url` | `XBIN_EXTERNAL_URL` |  | boot | the console's public base URL, e.g. https://xbin.corp.example — the stable address SSO redirect URIs are registered under (required for SSO login); also used for printed login/invite links. Empty on tunnel-only setups |
| VaultPassphrase |  | `XBIN_VAULT_PASSPHRASE` |  | boot | vault passphrase: auto-init/unseal the encryption barrier at boot (docs/auth.md §vault). Unset in production means the daemon starts SEALED (or LOCKED before first setup) until an admin unseals *(secret: never logged)* |
| LimitMem |  | `XBIN_LIMIT_MEM` | `2G` | boot | per-component cgroup v2 memory cap — plain bytes or a K/M/G/T suffix; active only when xbind's cgroup is delegated (systemd Delegate=yes / a container) |
| Bin |  | `XBIN_BIN` |  | boot | directory holding the bx CLI, put on terminals' PATH; default: next to the xbind binary, the repo's bin/ under --dev, then whatever is already on xbind's PATH |
| SDKPath |  | `XBIN_SDK_PATH` |  | `internal/deps` | the xbin Go SDK checkout for the generated go.work and the sandbox bind (default: /opt/xbin/sdk, else the repo's sdk/ under --dev) |
| LimitDisk |  | `XBIN_LIMIT_DISK` | `50G` | `internal/broker` | per-scope disk quota for tile state — plain bytes or a K/M/G/T suffix |
| Gocryptfs |  | `XBIN_GOCRYPTFS` |  | `internal/resenc` | the gocryptfs binary for encrypted file-backed resources (default: bundled next to xbind, then PATH; none ⇒ those resources stay plaintext) |
| FuseOverlayfs |  | `XBIN_FUSE_OVERLAYFS` |  | `internal/sandbox` | the fuse-overlayfs binary mounting sandbox roots (default: bundled next to xbind, then PATH; none ⇒ the kernel overlay) |
| SandboxDebug |  | `XBIN_SANDBOX_DEBUG` |  | `internal/sandbox` | set to anything to make the sandbox init log its steps |

The vault's boot mode follows from these settings, first match wins:
`XBIN_VAULT_PASSPHRASE` set → auto-unseal; `--insecure-vault` or `--no-auth` →
plaintext at rest; `--dev` → a built-in dev key (insecure); otherwise the
daemon starts sealed (a barrier exists) or locked (none yet) until an admin
unseals it — see [auth.md](/docs/auth.md).
