# App Review Notes — draft

What to paste into App Store Connect → the version → App Review Information →
Notes, for the first submission with app tunnels (D204). The owner fills the
`<…>` placeholders and sets up the demo workspace before submitting; keep this
file in step with the app (native/AGENTS.md → "App Store").

---

xbin is a client for self-hosted workspaces. People run the xbin server on
their own machine or a server they control, and use this app to reach their
own workspace: its apps, terminals and agent sessions. The app talks only to
workspaces its user adds; it has no accounts of ours and no content of its
own.

**Demo workspace.** `<https://demo.example.com>` — account `<reviewer>`,
password `<password>`. A computer browser signed in to it is needed only to
test pairing (below); signing in with the password works on its own.

**Signing in (password).** Log in → Enter workspace address →
`<https://demo.example.com>` → Continue → the username and password above.
The device then gets a key in its Secure Enclave; later sign-ins are a Face ID
prompt.

**App tunnels (pairing).** A workspace's administrator can require that the
app connects only through an encrypted tunnel. To test it:

1. On a computer, open `<https://demo.example.com>` in a browser and sign in
   as `<reviewer>`. Click **settings** (top right) → **add a device**. A large
   QR code appears; it works once, for 5 minutes.
2. In the app: Log in → Scan QR code (or copy the link under the code and
   paste it on Log in).
3. The app asks "Pair with <demo.example.com>?" and explains the tunnel and the Local
   Network prompt. Tap **Pair**.
4. The phone shows "Confirm on your computer: 123 456". The computer shows the
   same six digits; click **Codes match** there. The workspace opens in the
   app.

**Not a VPN.** The tunnel carries only this app's own traffic to the user's
own workspace. The app uses no NetworkExtension, installs no VPN
configuration and routes nothing for the system or other apps. While the app
is in the foreground it listens on 127.0.0.1 inside the app and forwards its
own requests (including its web views') through the tunnel; it stops when the
app leaves the foreground. The app has no background modes. It downloads no
code.

**Local Network.** iOS may ask whether xbin can use the local network. The
tunnel uses it to connect directly when the phone and the workspace are on
the same network; if the user declines, the app connects through the
workspace's relay instead and keeps working.

**Encryption.** Inside the tunnel the app runs the Noise protocol
(X25519, ChaChaPoly, SHA-256 through CryptoKit) end to end with the
workspace, over iroh's QUIC/TLS (rustls), from the open-source IrohLib.
These are standard algorithms; the export compliance answer is "uses
non-exempt encryption" (`ITSAppUsesNonExemptEncryption` = YES).

**Plain http.** xbin is a client for self-hosted workspaces; users enter
their own server's address, which may be plain http on a private network or
VPN. The app warns when an http address may travel unencrypted. That is why
`NSAllowsArbitraryLoads` is set.

**Privacy.** The app collects nothing for us. Policy:
https://xbin.dev/privacy.html. Open-source licences: Settings → About →
Open-source licences.
