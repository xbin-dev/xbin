//! xbin-iroh: xbind's iroh sidecar (D204, native/spec/tunnel.md §3,
//! plans/app-tunnel.md §2.1). xbind embeds it, runs it in a bare namespace
//! sandbox with the host network, and talks to it over stdin/stdout (JSON
//! lines, internal/apptunnel/wire) and one inherited SOCK_SEQPACKET socket
//! that carries the streams.
//!
//!   xbin-iroh serve --fd N    the endpoint (the secret key: stdin's first line)
//!   xbin-iroh client …        test mode: local connections → bi-streams to a ticket
//!   xbin-iroh version
//!
//! Logs go to stderr; XBIN_IROH_LOG sets the level (error|warn|info|debug|
//! trace; default info for the sidecar, warn for iroh).

mod client;
mod control;
mod factory;
mod header;
mod hooks;
mod serve;

use tracing_subscriber::filter::{LevelFilter, Targets};
use tracing_subscriber::prelude::*;

fn logging() {
    let lvl = std::env::var("XBIN_IROH_LOG").unwrap_or_default();
    let level = match lvl.as_str() {
        "error" => LevelFilter::ERROR,
        "warn" => LevelFilter::WARN,
        "debug" => LevelFilter::DEBUG,
        "trace" => LevelFilter::TRACE,
        _ => LevelFilter::INFO,
    };
    // iroh is chatty at info; it follows only when asked for more
    let iroh = if level > LevelFilter::INFO {
        level
    } else {
        level.min(LevelFilter::WARN)
    };
    let filter = Targets::new()
        .with_target("xbin_iroh", level)
        .with_default(iroh);
    tracing_subscriber::registry()
        .with(
            tracing_subscriber::fmt::layer()
                .with_writer(std::io::stderr)
                .with_ansi(false)
                .without_time(),
        )
        .with(filter)
        .init();
}

const USAGE: &str = "usage: xbin-iroh serve --fd N | client --ticket T --alpn A --listen tcp:H:P|unix:PATH [--addr IP:PORT]… | version";

fn main() {
    let args: Vec<String> = std::env::args().skip(1).collect();
    let code = match args.first().map(String::as_str) {
        Some("version") | Some("--version") => {
            println!("xbin-iroh {}", serve::VERSION);
            0
        }
        Some("serve") => {
            let fd = match args.get(1..) {
                Some([flag, n]) if flag == "--fd" => n.parse::<i32>().ok().filter(|n| *n > 2),
                _ => None,
            };
            match fd {
                Some(fd) => {
                    logging();
                    runtime().block_on(serve::run(fd))
                }
                None => {
                    eprintln!("{USAGE}");
                    2
                }
            }
        }
        Some("client") => match client::parse_args(&args[1..]) {
            Ok(a) => {
                logging();
                runtime().block_on(client::run(a))
            }
            Err(e) => {
                eprintln!("{e}");
                2
            }
        },
        _ => {
            eprintln!("{USAGE}");
            2
        }
    };
    std::process::exit(code);
}

fn runtime() -> tokio::runtime::Runtime {
    tokio::runtime::Builder::new_multi_thread()
        .worker_threads(2)
        .enable_all()
        .build()
        .expect("tokio runtime")
}
