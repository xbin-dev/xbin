# QA box — an auto-updating xbin instance

A self-updating "QA" deployment of xbind for a single Ubuntu VPS. It tracks the
newest **release tag** on GitHub, installs the prebuilt bundle, and restarts —
hands-off. Everything binds to **localhost**; you reach it through an SSH
port-forward. A tiny front proxy shows an "Updating…" page while xbind restarts.

```
browser ──ssh -L 9988──▶ 127.0.0.1:9988  xbin-qa-proxy ──▶ 127.0.0.1:8642  xbind
                                              │                    ▲
                                       reads /run/xbin-qa/state    │ reinstall + restart
                                              └──── xbin-qa-update.sh (systemd timer, ~2 min)
```

## Pieces

- **`proxy/main.go`** → `xbin-qa-proxy`: reverse-proxies `:9988` → `:8642`,
  serving the updating page whenever `/healthz` is not 200 or an update is in
  flight. Go stdlib only; does not add `X-Forwarded-Proto` (keeps the login
  cookie non-Secure over the http tunnel).
- **`xbin-qa-update.sh`**: polls `github.com/xbin-dev/xbin` tags, and on a newer
  release re-runs `https://xbin.dev/install.sh` (prebuilt, sha256-verified),
  preserving the vault passphrase, then restarts xbind.
- **systemd**: `xbin-qa-proxy.service`, `xbin-qa-update.service` +
  `xbin-qa-update.timer`.
- **`setup.sh`**: one-shot bootstrap (installs xbin + the proxy + the timer).

## Deploy

On the dev box:

```sh
GOOS=linux GOARCH=amd64 go build -o /tmp/xbin-qa/xbin-qa-proxy ./deploy/qa/proxy
cp deploy/qa/*.sh deploy/qa/*.service deploy/qa/*.timer /tmp/xbin-qa/
scp -r /tmp/xbin-qa ubuntu@<host>:/tmp/xbin-qa
ssh ubuntu@<host> 'sudo bash /tmp/xbin-qa/setup.sh'
```

Then, from your laptop: `ssh -L 9988:localhost:9988 ubuntu@<host>` and open
`http://localhost:9988/login?token=<token>` (setup prints the URL).
