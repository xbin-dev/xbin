//! `xbin-iroh serve --fd N`: xbind's iroh endpoint (plans/app-tunnel.md
//! §2.1). Accepts `xbin/tunnel/1` and `xbin/pair/1` connections the hook
//! admits; every bi-stream on them becomes a fresh socketpair whose far end
//! goes to xbind over the factory, the 48-byte header first, then a raw
//! splice. Noise runs in xbind, so this process only ever sees ciphertext.
//! Derived from dumbpipe's listen-unix (NOTICE).

use std::collections::HashMap;
use std::net::{Ipv4Addr, SocketAddr};
use std::os::fd::{AsRawFd, RawFd};
use std::str::FromStr;
use std::sync::atomic::{AtomicU64, Ordering};
use std::sync::{Arc, Mutex};
use std::time::{Duration, Instant};

use iroh::endpoint::{presets, Connection, QuicTransportConfig, RecvStream, SendStream, VarInt};
use iroh::{
    Endpoint, EndpointAddr, EndpointId, RelayConfig, RelayMap, RelayMode, RelayUrl, SecretKey,
    Watcher,
};
use iroh_tickets::{endpoint::EndpointTicket, Ticket};
use tokio::io::{AsyncBufReadExt, AsyncWriteExt, BufReader};
use tokio::net::UnixStream;
use tokio::sync::mpsc;

use crate::control::{self, ConnInfo, Event, Init, Relay};
use crate::factory::Factory;
use crate::header::{self, ALPN_PAIR_NAME, ALPN_SELFTEST_NAME, ALPN_TUNNEL_NAME};
use crate::hooks::{Gate, Hook, Policy, REFUSED};

pub const VERSION: &str = concat!(env!("CARGO_PKG_VERSION"), "+iroh1.4.1");

/// How long a stream may wait for room in xbind's factory queue.
const HANDOFF: Duration = Duration::from_secs(5);
/// Live connections the sidecar keeps at once (xbind caps its own too).
const MAX_CONNS: usize = 1024;
/// How long ready waits for n0 to pick a home relay before saying so.
const HOME_RELAY_WAIT: Duration = Duration::from_secs(10);

/// The stdout side: a bounded queue a single writer drains, so lines never
/// interleave. Best-effort events (conn, reject) are dropped when xbind
/// stops reading; the rest wait.
#[derive(Debug, Clone)]
pub struct Events(mpsc::Sender<Event>);

impl Events {
    pub fn new() -> (Self, mpsc::Receiver<Event>) {
        let (tx, rx) = mpsc::channel(4096);
        (Events(tx), rx)
    }
    pub fn try_emit(&self, e: Event) {
        let _ = self.0.try_send(e);
    }
    pub async fn emit(&self, e: Event) {
        let _ = self.0.send(e).await;
    }
}

/// Writes events to w, one line each, flushed.
pub async fn write_events<W: tokio::io::AsyncWrite + Unpin>(
    mut rx: mpsc::Receiver<Event>,
    mut w: W,
) {
    while let Some(e) = rx.recv().await {
        let mut line = e.line();
        line.push('\n');
        if w.write_all(line.as_bytes()).await.is_err() || w.flush().await.is_err() {
            return;
        }
    }
}

pub fn parse_ids(ids: &[String]) -> (Vec<EndpointId>, Vec<String>) {
    let mut ok = Vec::new();
    let mut bad = Vec::new();
    for s in ids {
        match EndpointId::from_str(s) {
            Ok(id) => ok.push(id),
            Err(_) => bad.push(s.clone()),
        }
    }
    (ok, bad)
}

pub fn parse_secret(hex_key: &str) -> Result<SecretKey, String> {
    let b = hex::decode(hex_key.trim()).map_err(|_| "init: secretKey is not hex".to_string())?;
    let seed: [u8; 32] = b
        .try_into()
        .map_err(|_| "init: secretKey is not 32 bytes".to_string())?;
    Ok(SecretKey::from_bytes(&seed))
}

/// The transport limits: bi-streams only, 128 at once; no uni-streams, no
/// datagrams.
pub fn transport() -> QuicTransportConfig {
    QuicTransportConfig::builder()
        .max_concurrent_bidi_streams(VarInt::from_u32(128))
        .max_concurrent_uni_streams(VarInt::from_u32(0))
        .datagram_receive_buffer_size(None)
        .build()
}

/// The endpoint builder for a relay mode: N0's preset for n0; Minimal plus
/// exactly the one custom relay (token, QUIC address discovery) for
/// custom; Minimal on loopback with no relay for disabled (tests).
pub fn builder(relay: &Relay) -> Result<iroh::endpoint::Builder, String> {
    match relay.mode.as_str() {
        "n0" => Ok(Endpoint::builder(presets::N0)),
        "custom" => {
            let url = RelayUrl::from_str(&relay.url)
                .map_err(|e| format!("relay url {:?}: {e}", relay.url))?;
            let mut cfg = RelayConfig::from(url);
            if !relay.token.is_empty() {
                cfg = cfg.with_auth_token(relay.token.clone());
            }
            Ok(Endpoint::builder(presets::Minimal)
                .relay_mode(RelayMode::Custom(RelayMap::from(cfg))))
        }
        "disabled" => Endpoint::builder(presets::Minimal)
            .relay_mode(RelayMode::Disabled)
            .clear_ip_transports()
            .bind_addr(SocketAddr::from((Ipv4Addr::LOCALHOST, 0)))
            .map_err(|e| format!("bind addr: {e}")),
        m => Err(format!("relay mode {m:?} (n0 | custom | disabled)")),
    }
}

/// One live connection, for status, close and the conn events.
struct Live {
    remote: EndpointId,
    conn: Connection,
}

pub struct Server {
    ep: Endpoint,
    relay: Relay,
    gate: Arc<Gate>,
    events: Events,
    factory: Arc<Factory>,
    conns: Mutex<HashMap<u64, Live>>,
    serial: AtomicU64,
}

fn path_of(conn: &Connection) -> (String, i64) {
    let paths = conn.paths();
    for p in paths.iter() {
        if p.is_selected() {
            let kind = if p.is_relay() { "relay" } else { "direct" };
            return (kind.into(), p.rtt().as_millis() as i64);
        }
    }
    (String::new(), 0)
}

impl Server {
    /// The address the ticket and the selftest dial: the home relay only
    /// (a relay-only ticket stays short and private), or with relays
    /// disabled (tests), the loopback sockets.
    fn dial_addr(&self) -> EndpointAddr {
        let mut addr = EndpointAddr::new(self.ep.id());
        if self.relay.mode == "disabled" {
            for s in self.ep.bound_sockets() {
                if s.is_ipv4() {
                    addr = addr.with_ip_addr(SocketAddr::from((Ipv4Addr::LOCALHOST, s.port())));
                }
            }
        } else if let Some(url) = self.home_relay() {
            addr = addr.with_relay_url(url);
        }
        addr
    }

    fn home_relay(&self) -> Option<RelayUrl> {
        if self.relay.mode == "custom" {
            return RelayUrl::from_str(&self.relay.url).ok();
        }
        self.ep
            .home_relay_status()
            .get()
            .first()
            .map(|s| s.url().clone())
    }

    fn ready(&self) -> Event {
        Event {
            endpoint_id: hex::encode(self.ep.id().as_bytes()),
            ticket: EndpointTicket::new(self.dial_addr()).encode_string(),
            home_relay: self.home_relay().map(|u| u.to_string()).unwrap_or_default(),
            version: VERSION.into(),
            ..Event::new("ready")
        }
    }

    fn status(&self) -> Event {
        let relays = self.ep.home_relay_status().get();
        let conns = self.conns.lock().unwrap();
        let mut list: Vec<ConnInfo> = conns
            .iter()
            .map(|(n, l)| {
                let (path, rtt_ms) = path_of(&l.conn);
                ConnInfo {
                    endpoint: hex::encode(l.remote.as_bytes()),
                    conn: *n,
                    path,
                    rtt_ms,
                }
            })
            .collect();
        list.sort_by_key(|c| c.conn);
        Event {
            home_relay: relays
                .first()
                .map(|s| s.url().to_string())
                .unwrap_or_default(),
            relay_connected: relays.iter().any(|s| s.is_connected()),
            conns: list,
            ..Event::new("status")
        }
    }

    fn set_allow(&self, ids: &[String]) {
        let (ok, bad) = parse_ids(ids);
        if !bad.is_empty() {
            self.events.try_emit(Event::error(
                format!("allow: {} entries are not EndpointIds", bad.len()),
                false,
            ));
        }
        let set: std::collections::HashSet<EndpointId> = ok.into_iter().collect();
        // A tunnel connection whose endpoint left the list goes with it.
        for l in self.conns.lock().unwrap().values() {
            if l.conn.alpn() == ALPN_TUNNEL_NAME && !set.contains(&l.remote) {
                l.conn
                    .close(VarInt::from_u32(REFUSED), b"no longer allowed");
            }
        }
        self.gate.policy.write().unwrap().allow = set;
    }

    fn close_endpoint(&self, endpoint: &str) {
        let Ok(id) = EndpointId::from_str(endpoint) else {
            self.events.try_emit(Event::error(
                format!("close: {endpoint:?} is not an EndpointId"),
                false,
            ));
            return;
        };
        for l in self.conns.lock().unwrap().values() {
            if l.remote == id {
                l.conn.close(VarInt::from_u32(REFUSED), b"revoked");
            }
        }
    }

    async fn accept_loop(self: Arc<Self>) {
        while let Some(incoming) = self.ep.accept().await {
            if self.conns.lock().unwrap().len() >= MAX_CONNS {
                incoming.refuse();
                continue;
            }
            let s = self.clone();
            tokio::spawn(async move {
                let accepting = match incoming.accept() {
                    Ok(a) => a,
                    Err(e) => {
                        tracing::debug!("incoming: {e}");
                        return;
                    }
                };
                match accepting.await {
                    Ok(conn) => s.serve_conn(conn).await,
                    Err(e) => tracing::debug!("handshake: {e}"), // includes the hook's refusals
                }
            });
        }
    }

    async fn serve_conn(self: Arc<Self>, conn: Connection) {
        let remote = conn.remote_id();
        if conn.alpn() == ALPN_SELFTEST_NAME {
            // the sidecar's own probe: it closes when it has measured
            let _ = tokio::time::timeout(Duration::from_secs(30), conn.closed()).await;
            return;
        }
        let Some(code) = header::alpn_code(conn.alpn()) else {
            conn.close(VarInt::from_u32(REFUSED), b"unknown alpn");
            return;
        };
        let alpn = String::from_utf8_lossy(conn.alpn()).into_owned();
        let serial = self.serial.fetch_add(1, Ordering::Relaxed) + 1;
        let ep_hex = hex::encode(remote.as_bytes());
        self.conns.lock().unwrap().insert(
            serial,
            Live {
                remote,
                conn: conn.clone(),
            },
        );
        let (path, _) = path_of(&conn);
        self.events.try_emit(Event {
            endpoint: ep_hex.clone(),
            conn: serial,
            open: true,
            path,
            alpn,
            ..Event::new("conn")
        });
        let hdr = header::encode(code, remote.as_bytes(), serial);
        loop {
            match conn.accept_bi().await {
                Ok((send, recv)) => {
                    let factory = self.factory.clone();
                    tokio::spawn(async move {
                        if let Err(e) = splice(&factory, hdr, send, recv).await {
                            tracing::debug!("stream: {e}");
                        }
                    });
                }
                Err(e) => {
                    tracing::debug!(conn = serial, "closed: {e}");
                    break;
                }
            }
        }
        self.conns.lock().unwrap().remove(&serial);
        self.events.try_emit(Event {
            endpoint: ep_hex,
            conn: serial,
            ..Event::new("conn")
        });
    }

    /// Dials this endpoint from a throwaway one, through the home relay
    /// only (no IP transport on the probe), and reports what it saw.
    async fn selftest(self: Arc<Self>) -> Event {
        let key = SecretKey::generate();
        let probe_id = key.public();
        let mut ev = Event::new("selftest");
        let mut b = match builder(&self.relay) {
            Ok(b) => b,
            Err(e) => {
                ev.error = e;
                return ev;
            }
        };
        if self.relay.mode == "n0" {
            // the probe dials the relay URL itself: no lookups, no publishing
            b = Endpoint::builder(presets::Minimal).relay_mode(RelayMode::Default);
        }
        if self.relay.mode != "disabled" {
            b = b.clear_ip_transports();
        }
        let addr = self.dial_addr();
        if addr.is_empty() {
            ev.error = "no home relay yet".into();
            return ev;
        }
        self.gate.policy.write().unwrap().selftest.insert(probe_id);
        let started = Instant::now();
        let res = tokio::time::timeout(Duration::from_secs(20), async {
            let probe = b
                .secret_key(key)
                .bind()
                .await
                .map_err(|e| format!("probe endpoint: {e}"))?;
            let r = probe.connect(addr, ALPN_SELFTEST_NAME).await;
            let out = match r {
                Ok(conn) => {
                    let (path, rtt) = path_of(&conn);
                    conn.close(VarInt::from_u32(0), b"ok");
                    Ok((path, rtt))
                }
                Err(e) => Err(format!("dial: {e}")),
            };
            probe.close().await;
            out
        })
        .await;
        self.gate.policy.write().unwrap().selftest.remove(&probe_id);
        match res {
            Ok(Ok((path, rtt))) => {
                ev.reachable = true;
                ev.path = path;
                ev.rtt_ms = if rtt > 0 {
                    rtt
                } else {
                    started.elapsed().as_millis() as i64
                };
            }
            Ok(Err(e)) => ev.error = e,
            Err(_) => ev.error = "timed out".into(),
        }
        ev
    }
}

/// One stream: a fresh socketpair, the header on our end, the other end to
/// xbind, then bytes both ways until both sides are done.
async fn splice(
    factory: &Factory,
    hdr: [u8; header::HEADER_LEN],
    mut send: SendStream,
    mut recv: RecvStream,
) -> std::io::Result<()> {
    let (mut mine, theirs) = UnixStream::pair()?;
    mine.write_all(&hdr).await?;
    let handed = tokio::time::timeout(HANDOFF, factory.send(theirs.as_raw_fd())).await;
    drop(theirs);
    match handed {
        Ok(Ok(())) => {}
        Ok(Err(e)) => {
            let _ = send.reset(VarInt::from_u32(0));
            let _ = recv.stop(VarInt::from_u32(0));
            return Err(e);
        }
        Err(_) => {
            let _ = send.reset(VarInt::from_u32(0));
            let _ = recv.stop(VarInt::from_u32(0));
            return Err(std::io::Error::other(
                "xbind did not take the stream in time",
            ));
        }
    }
    let (mut ur, mut uw) = mine.into_split();
    let inbound = async {
        let r = tokio::io::copy(&mut recv, &mut uw).await;
        if r.is_err() {
            let _ = recv.stop(VarInt::from_u32(0));
        }
        let _ = uw.shutdown().await;
        r
    };
    let outbound = async {
        let r = tokio::io::copy(&mut ur, &mut send).await;
        match r {
            Ok(_) => {
                let _ = send.finish();
            }
            Err(_) => {
                let _ = send.reset(VarInt::from_u32(0));
            }
        }
        r
    };
    let (a, b) = tokio::join!(inbound, outbound);
    a?;
    b?;
    Ok(())
}

/// Binds the endpoint for an init and returns the server (not yet
/// accepting).
pub async fn bind(
    init: &Init,
    factory: Arc<Factory>,
    events: Events,
) -> Result<Arc<Server>, String> {
    let key = parse_secret(&init.secret_key)?;
    let (allow, bad) = parse_ids(init.allow.as_deref().unwrap_or_default());
    if !bad.is_empty() {
        events
            .emit(Event::error(
                format!("init: {} allow entries are not EndpointIds", bad.len()),
                false,
            ))
            .await;
    }
    let gate = Gate::new(
        Policy {
            allow: allow.into_iter().collect(),
            pairing: init.pairing,
            ..Default::default()
        },
        events.clone(),
    );
    let ep = builder(&init.relay)?
        .secret_key(key)
        .alpns(vec![
            ALPN_TUNNEL_NAME.to_vec(),
            ALPN_PAIR_NAME.to_vec(),
            ALPN_SELFTEST_NAME.to_vec(),
        ])
        .transport_config(transport())
        .hooks(Hook(gate.clone()))
        .bind()
        .await
        .map_err(|e| format!("bind: {e}"))?;
    Ok(Arc::new(Server {
        ep,
        relay: init.relay.clone(),
        gate,
        events,
        factory,
        conns: Mutex::new(HashMap::new()),
        serial: AtomicU64::new(0),
    }))
}

impl Server {
    /// Says ready (after n0 has picked a home relay, or a while), then
    /// follows the relay: a status line on every change, and ready again if
    /// the home relay (so the ticket) moves.
    async fn announce(self: Arc<Self>) {
        if self.relay.mode == "n0" {
            let mut w = self.ep.home_relay_status();
            let _ = tokio::time::timeout(HOME_RELAY_WAIT, async {
                while w.get().is_empty() {
                    if w.updated().await.is_err() {
                        break;
                    }
                }
            })
            .await;
        }
        let mut home = self.home_relay();
        self.events.emit(self.ready()).await;
        if self.relay.mode == "disabled" {
            return;
        }
        let mut w = self.ep.home_relay_status();
        let mut last = (String::new(), false);
        loop {
            let st = self.status();
            let now = (st.home_relay.clone(), st.relay_connected);
            if now != last {
                self.events.emit(st).await;
                last = now;
            }
            let h = self.home_relay();
            if h.is_some() && h != home {
                home = h;
                self.events.emit(self.ready()).await;
            }
            if w.updated().await.is_err() {
                return;
            }
        }
    }

    async fn command(self: &Arc<Self>, c: control::Command) {
        match c.t.as_str() {
            "allow" => {
                self.set_allow(c.allow.as_deref().unwrap_or_default());
                self.events
                    .emit(Event {
                        seq: c.seq,
                        ..Event::new("ack")
                    })
                    .await;
            }
            "pairing" => self.gate.policy.write().unwrap().pairing = c.open,
            "close" => self.close_endpoint(&c.endpoint),
            "status" => self.events.emit(self.status()).await,
            "selftest" => {
                let s = self.clone();
                tokio::spawn(async move {
                    let ev = s.clone().selftest().await;
                    s.events.emit(ev).await;
                });
            }
            other => self
                .events
                .try_emit(Event::error(format!("unknown command {other:?}"), false)),
        }
    }
}

/// Runs the sidecar until stdin ends, the factory closes, or SIGTERM.
pub async fn run(fd: RawFd) -> i32 {
    let (events, rx) = Events::new();
    let writer = tokio::spawn(write_events(rx, tokio::io::stdout()));
    let code = run_inner(fd, events).await;
    // the last lines (a fatal error) out; the writer itself never ends while
    // the endpoint's hook holds a sender
    let _ = tokio::time::timeout(Duration::from_millis(300), writer).await;
    code
}

async fn run_inner(fd: RawFd, events: Events) -> i32 {
    let fatal = |events: Events, msg: String| async move {
        tracing::error!("{msg}");
        events.emit(Event::error(msg, true)).await;
        1
    };
    let factory = match unsafe { Factory::from_raw(fd) } {
        Ok(f) => Arc::new(f),
        Err(e) => return fatal(events, format!("factory: {e}")).await,
    };
    let mut lines = BufReader::new(tokio::io::stdin()).lines();
    let init = match lines.next_line().await {
        Ok(Some(l)) => match control::parse_init(&l) {
            Ok(i) => i,
            Err(e) => return fatal(events, e).await,
        },
        Ok(None) => return fatal(events, "stdin closed before init".into()).await,
        Err(e) => return fatal(events, format!("stdin: {e}")).await,
    };
    let server = match bind(&init, factory.clone(), events.clone()).await {
        Ok(s) => s,
        Err(e) => return fatal(events, e).await,
    };
    drop(init); // the key lives in the endpoint only
    tracing::info!(id = %server.ep.id(), relay = %server.relay.mode, "endpoint bound");
    tokio::spawn(server.clone().accept_loop());
    let announce = tokio::spawn(server.clone().announce());

    let mut term = match tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate()) {
        Ok(s) => s,
        Err(e) => return fatal(events, format!("sigterm handler: {e}")).await,
    };
    let mut int = tokio::signal::unix::signal(tokio::signal::unix::SignalKind::interrupt()).ok();
    loop {
        tokio::select! {
            line = lines.next_line() => match line {
                Ok(Some(l)) if l.trim().is_empty() => {}
                Ok(Some(l)) => match control::parse_command(&l) {
                    Ok(c) => server.command(c).await,
                    Err(e) => events.try_emit(Event::error(e, false)),
                },
                Ok(None) | Err(_) => { tracing::info!("stdin closed: stopping"); break }
            },
            _ = factory.closed() => { tracing::info!("xbind closed the factory: stopping"); break }
            _ = term.recv() => { tracing::info!("SIGTERM: stopping"); break }
            _ = async { match int.as_mut() { Some(s) => { s.recv().await; } None => std::future::pending().await } } => break,
        }
    }
    announce.abort();
    let _ = tokio::time::timeout(Duration::from_secs(1), server.ep.close()).await;
    0
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::factory::recv_fd;
    use std::io::{Read, Write};
    use std::os::fd::FromRawFd;
    use std::os::unix::net::UnixStream as StdUnixStream;

    fn seqpacket() -> (RawFd, RawFd) {
        let mut fds = [0; 2];
        assert_eq!(
            unsafe {
                libc::socketpair(
                    libc::AF_UNIX,
                    libc::SOCK_SEQPACKET | libc::SOCK_CLOEXEC,
                    0,
                    fds.as_mut_ptr(),
                )
            },
            0
        );
        (fds[0], fds[1])
    }

    fn init(key: &SecretKey, allow: &[EndpointId], pairing: bool) -> Init {
        control::parse_init(
            &serde_json::json!({
                "t": "init", "v": 1,
                "secretKey": hex::encode(key.to_bytes()),
                "relay": {"mode": "disabled"},
                "allow": allow.iter().map(|i| hex::encode(i.as_bytes())).collect::<Vec<_>>(),
                "pairing": pairing,
            })
            .to_string(),
        )
        .unwrap()
    }

    async fn client() -> (Endpoint, EndpointId) {
        let ep = builder(&Relay {
            mode: "disabled".into(),
            ..Default::default()
        })
        .unwrap()
        .bind()
        .await
        .unwrap();
        let id = ep.id();
        (ep, id)
    }

    fn recv_blocking(fd: RawFd) -> StdUnixStream {
        let got = recv_fd(fd).unwrap().expect("a stream");
        let s = StdUnixStream::from(got);
        // O_NONBLOCK travels with the file description (tokio's pair)
        s.set_nonblocking(false).unwrap();
        s.set_read_timeout(Some(Duration::from_secs(10))).unwrap();
        s
    }

    /// The hook and the splice end to end on loopback, with xbind's side
    /// played by recvmsg on the factory.
    #[tokio::test(flavor = "multi_thread")]
    async fn streams_reach_xbind_with_the_header() {
        let (xbind, child) = seqpacket();
        let factory = Arc::new(unsafe { Factory::from_raw(child) }.unwrap());
        let (events, mut rx) = Events::new();
        let (dev, dev_id) = client().await;
        let (stranger, _) = client().await;
        let key = SecretKey::generate();
        let srv = bind(&init(&key, &[dev_id], false), factory, events)
            .await
            .unwrap();
        tokio::spawn(srv.clone().accept_loop());
        tokio::spawn(srv.clone().announce());
        let ready = rx.recv().await.unwrap();
        assert_eq!(ready.t, "ready");
        assert_eq!(ready.endpoint_id, hex::encode(key.public().as_bytes()));
        let ticket = EndpointTicket::decode_string(&ready.ticket).unwrap();
        let addr = ticket.endpoint_addr().clone();
        assert!(
            addr.ip_addrs().next().is_some(),
            "disabled: the ticket carries loopback"
        );

        // an allowed device: a stream arrives with the header, bytes both ways
        let conn = dev.connect(addr.clone(), ALPN_TUNNEL_NAME).await.unwrap();
        let (mut send, mut recv) = conn.open_bi().await.unwrap();
        send.write_all(b"hello xbind").await.unwrap();
        let mut s = tokio::task::spawn_blocking(move || recv_blocking(xbind))
            .await
            .unwrap();
        let mut hdr = [0u8; 48];
        s.read_exact(&mut hdr).unwrap();
        assert_eq!(&hdr[0..4], b"XBI1");
        assert_eq!(hdr[5], header::ALPN_TUNNEL);
        assert_eq!(&hdr[8..40], dev_id.as_bytes());
        assert_eq!(u64::from_be_bytes(hdr[40..48].try_into().unwrap()), 1);
        let mut buf = [0u8; 11];
        s.read_exact(&mut buf).unwrap();
        assert_eq!(&buf, b"hello xbind");
        s.write_all(b"hello phone").unwrap();
        let mut back = [0u8; 11];
        recv.read_exact(&mut back).await.unwrap();
        assert_eq!(&back, b"hello phone");
        // half-close travels both ways
        send.finish().unwrap();
        let mut rest = Vec::new();
        let mut s2 = s.try_clone().unwrap();
        tokio::task::spawn_blocking(move || s2.read_to_end(&mut rest).map(|_| rest))
            .await
            .unwrap()
            .map(|r| assert!(r.is_empty()))
            .unwrap();
        s.shutdown(std::net::Shutdown::Write).unwrap();
        let tail = recv.read_to_end(1024).await.unwrap();
        assert!(tail.is_empty());
        let ev = rx.recv().await.unwrap();
        assert_eq!(
            (ev.t.as_str(), ev.conn, ev.open, ev.alpn.as_str()),
            ("conn", 1, true, "xbin/tunnel/1")
        );

        // a stranger: refused by the hook, never handed to xbind
        let c2 = stranger.connect(addr.clone(), ALPN_TUNNEL_NAME).await;
        if let Ok(c2) = c2 {
            let err = tokio::time::timeout(Duration::from_secs(10), c2.closed())
                .await
                .unwrap();
            assert!(format!("{err}").contains("403"), "closed with 403: {err}");
        }
        let ev = rx.recv().await.unwrap();
        assert_eq!(
            (ev.t.as_str(), ev.alpn.as_str()),
            ("reject", "xbin/tunnel/1")
        );
        // pairing closed: refused too; opened: admitted with the pair code
        srv.command(control::parse_command(r#"{"t":"pairing","open":true}"#).unwrap())
            .await;
        let pc = stranger
            .connect(addr.clone(), ALPN_PAIR_NAME)
            .await
            .unwrap();
        let (mut ps, _pr) = pc.open_bi().await.unwrap();
        ps.write_all(b"x").await.unwrap();
        let mut s3 = tokio::task::spawn_blocking(move || recv_blocking(xbind))
            .await
            .unwrap();
        s3.read_exact(&mut hdr).unwrap();
        assert_eq!(hdr[5], header::ALPN_PAIR);
        assert_eq!(u64::from_be_bytes(hdr[40..48].try_into().unwrap()), 2);

        // allow replaces the list and is acknowledged; the device's
        // connection goes with it
        srv.command(control::parse_command(r#"{"t":"allow","seq":5}"#).unwrap())
            .await;
        loop {
            let ev = rx.recv().await.unwrap();
            if ev.t == "ack" {
                assert_eq!(ev.seq, 5);
                break;
            }
        }
        let err = tokio::time::timeout(Duration::from_secs(10), conn.closed())
            .await
            .unwrap();
        assert!(format!("{err}").contains("403"), "{err}");

        // status, then the selftest (direct here: no relay)
        srv.command(control::parse_command(r#"{"t":"status"}"#).unwrap())
            .await;
        srv.command(control::parse_command(r#"{"t":"selftest"}"#).unwrap())
            .await;
        let mut saw = (false, false);
        while !(saw.0 && saw.1) {
            let ev = tokio::time::timeout(Duration::from_secs(30), rx.recv())
                .await
                .unwrap()
                .unwrap();
            match ev.t.as_str() {
                "status" => saw.0 = true,
                "selftest" => {
                    assert!(ev.reachable, "selftest: {ev:?}");
                    saw.1 = true;
                }
                _ => {}
            }
        }
        unsafe { drop(std::os::fd::OwnedFd::from_raw_fd(xbind)) };
    }
}
