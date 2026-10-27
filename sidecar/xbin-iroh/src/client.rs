//! `xbin-iroh client`: the test-mode device side. Every local connection
//! (TCP or unix) becomes one bi-stream to the ticket's endpoint on one iroh
//! connection, the way the app's loopback forwarder works. QUIC announces a
//! stream with its first bytes, so the local side writes first (Noise's
//! initiator does).
//!
//! stdin, optional first line: `{"secretKey":"<hex>","relay":{"mode",
//! "url","token"}}` — the relay token travels here, never in argv. Without
//! it (stdin closed or empty): a fresh key, and the ticket's relay with no
//! token (or direct only when the ticket names none). stdout: one line
//! `{"t":"listening","endpointId":…,"addr":…}`, then `error` lines.

use std::net::SocketAddr;
use std::str::FromStr;
use std::sync::Arc;
use std::time::Duration;

use iroh::endpoint::{presets, Connection, VarInt};
use iroh::{Endpoint, EndpointAddr, RelayMode, SecretKey};
use iroh_tickets::endpoint::EndpointTicket;
use serde::Deserialize;
use tokio::io::{AsyncBufReadExt, AsyncRead, AsyncWrite, AsyncWriteExt, BufReader};
use tokio::net::{TcpListener, UnixListener};
use tokio::sync::Mutex;

use crate::control::Relay;
use crate::serve::{builder, parse_secret, transport};

#[derive(Debug, Default, Deserialize)]
struct ClientInit {
    #[serde(default, rename = "secretKey")]
    secret_key: String,
    #[serde(default)]
    relay: Option<Relay>,
}

pub struct Args {
    pub ticket: String,
    pub alpn: String,
    pub listen: String,     // tcp:HOST:PORT | unix:PATH
    pub addrs: Vec<String>, // extra direct addresses for the ticket's endpoint
}

fn usage(msg: &str) -> String {
    format!("client: {msg} (xbin-iroh client --ticket T --alpn A --listen tcp:H:P|unix:PATH [--addr IP:PORT]…)")
}

pub fn parse_args(args: &[String]) -> Result<Args, String> {
    let mut a = Args {
        ticket: String::new(),
        alpn: "xbin/tunnel/1".into(),
        listen: String::new(),
        addrs: Vec::new(),
    };
    let mut it = args.iter();
    while let Some(k) = it.next() {
        let mut val = || {
            it.next()
                .cloned()
                .ok_or_else(|| usage(&format!("{k} needs a value")))
        };
        match k.as_str() {
            "--ticket" => a.ticket = val()?,
            "--alpn" => a.alpn = val()?,
            "--listen" => a.listen = val()?,
            "--addr" => a.addrs.push(val()?),
            _ => return Err(usage(&format!("unknown argument {k:?}"))),
        }
    }
    if a.ticket.is_empty() || a.listen.is_empty() {
        return Err(usage("--ticket and --listen are required"));
    }
    Ok(a)
}

struct Dialer {
    ep: Endpoint,
    addr: EndpointAddr,
    alpn: Vec<u8>,
    conn: Mutex<Option<Connection>>,
}

impl Dialer {
    /// The shared connection, dialed on first use and again once closed.
    async fn conn(&self) -> Result<Connection, String> {
        let mut g = self.conn.lock().await;
        if let Some(c) = g.as_ref() {
            if c.close_reason().is_none() {
                return Ok(c.clone());
            }
        }
        let c = self
            .ep
            .connect(self.addr.clone(), &self.alpn)
            .await
            .map_err(|e| format!("connect: {e}"))?;
        *g = Some(c.clone());
        Ok(c)
    }
}

fn say(v: serde_json::Value) {
    println!("{v}");
}

async fn forward<R, W>(d: Arc<Dialer>, mut lr: R, mut lw: W)
where
    R: AsyncRead + Unpin,
    W: AsyncWrite + Unpin,
{
    let conn = match d.conn().await {
        Ok(c) => c,
        Err(e) => {
            say(serde_json::json!({"t": "error", "msg": e}));
            return;
        }
    };
    let (mut send, mut recv) = match conn.open_bi().await {
        Ok(s) => s,
        Err(e) => {
            say(serde_json::json!({"t": "error", "msg": format!("open stream: {e}")}));
            return;
        }
    };
    let out = async {
        let r = tokio::io::copy(&mut lr, &mut send).await;
        match r {
            Ok(_) => {
                let _ = send.finish();
            }
            Err(_) => {
                let _ = send.reset(VarInt::from_u32(0));
            }
        }
    };
    let inb = async {
        let _ = tokio::io::copy(&mut recv, &mut lw).await;
        let _ = lw.shutdown().await;
    };
    tokio::join!(out, inb);
}

pub async fn run(args: Args) -> i32 {
    match run_inner(args).await {
        Ok(()) => 0,
        Err(e) => {
            eprintln!("xbin-iroh client: {e}");
            say(serde_json::json!({"t": "error", "msg": e, "fatal": true}));
            1
        }
    }
}

async fn run_inner(args: Args) -> Result<(), String> {
    let ticket = EndpointTicket::from_str(&args.ticket).map_err(|e| format!("ticket: {e}"))?;
    let mut addr = ticket.endpoint_addr().clone();
    for a in &args.addrs {
        let sa = SocketAddr::from_str(a).map_err(|e| format!("--addr {a}: {e}"))?;
        addr = addr.with_ip_addr(sa);
    }
    let mut stdin = BufReader::new(tokio::io::stdin()).lines();
    let init: ClientInit =
        match tokio::time::timeout(Duration::from_secs(5), stdin.next_line()).await {
            Ok(Ok(Some(l))) if !l.trim().is_empty() => {
                serde_json::from_str(&l).map_err(|e| format!("stdin: {e}"))?
            }
            _ => ClientInit::default(),
        };
    let key = if init.secret_key.is_empty() {
        SecretKey::generate()
    } else {
        parse_secret(&init.secret_key)?
    };
    let relay = match init.relay {
        Some(r) => r,
        None => match addr.relay_urls().next() {
            Some(u) => Relay {
                mode: "custom".into(),
                url: u.to_string(),
                token: String::new(),
            },
            None => Relay {
                mode: "disabled".into(),
                ..Default::default()
            },
        },
    };
    // The device never publishes or looks up: Minimal, dial the ticket.
    let b = if relay.mode == "n0" {
        Endpoint::builder(presets::Minimal).relay_mode(RelayMode::Default)
    } else {
        builder(&relay)?
    };
    let ep = b
        .secret_key(key)
        .transport_config(transport())
        .bind()
        .await
        .map_err(|e| format!("bind: {e}"))?;
    let d = Arc::new(Dialer {
        ep: ep.clone(),
        addr,
        alpn: args.alpn.into_bytes(),
        conn: Mutex::new(None),
    });
    let id = hex::encode(ep.id().as_bytes());
    if let Some(hp) = args.listen.strip_prefix("tcp:") {
        let l = TcpListener::bind(hp)
            .await
            .map_err(|e| format!("listen {hp}: {e}"))?;
        let at = l.local_addr().map_err(|e| e.to_string())?;
        say(serde_json::json!({"t": "listening", "endpointId": id, "addr": at.to_string()}));
        loop {
            let (s, _) = l.accept().await.map_err(|e| format!("accept: {e}"))?;
            let (r, w) = s.into_split();
            tokio::spawn(forward(d.clone(), r, w));
        }
    } else if let Some(p) = args.listen.strip_prefix("unix:") {
        let _ = std::fs::remove_file(p);
        let l = UnixListener::bind(p).map_err(|e| format!("listen {p}: {e}"))?;
        say(serde_json::json!({"t": "listening", "endpointId": id, "addr": p}));
        loop {
            let (s, _) = l.accept().await.map_err(|e| format!("accept: {e}"))?;
            let (r, w) = s.into_split();
            tokio::spawn(forward(d.clone(), r, w));
        }
    } else {
        Err(usage("--listen is tcp:HOST:PORT or unix:PATH"))
    }
}
