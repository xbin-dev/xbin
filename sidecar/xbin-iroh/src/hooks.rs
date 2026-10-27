//! Who may connect (native/spec/tunnel.md §3): the after-handshake hook
//! admits `xbin/tunnel/1` only from EndpointIds on xbind's allowlist (paired
//! devices), `xbin/pair/1` only while a pairing is open, and the selftest
//! ALPN only from the throwaway endpoint the sidecar is dialing itself
//! with. Everything else is closed with 403 before a stream is accepted;
//! xbind hears of it as a rate-limited `reject` event.

use std::collections::{HashMap, HashSet};
use std::sync::{Arc, Mutex, RwLock};
use std::time::{Duration, Instant};

use iroh::endpoint::{AfterHandshakeOutcome, Connection, EndpointHooks, VarInt};
use iroh::EndpointId;

use crate::control::Event;
use crate::header::{ALPN_PAIR_NAME, ALPN_SELFTEST_NAME, ALPN_TUNNEL_NAME};
use crate::serve::Events;

/// The QUIC close code of a refused connection.
pub const REFUSED: u32 = 403;

#[derive(Debug, Default)]
pub struct Policy {
    pub allow: HashSet<EndpointId>,
    pub pairing: bool,
    pub selftest: HashSet<EndpointId>,
}

impl Policy {
    pub fn admits(&self, alpn: &[u8], remote: &EndpointId) -> bool {
        match alpn {
            ALPN_TUNNEL_NAME => self.allow.contains(remote),
            ALPN_PAIR_NAME => self.pairing,
            ALPN_SELFTEST_NAME => self.selftest.contains(remote),
            _ => false,
        }
    }
}

/// At most one reject event per endpoint and ALPN a minute, and at most
/// `BURST` a minute overall: a scanner can't flood xbind's log.
const PER_KEY: Duration = Duration::from_secs(60);
const WINDOW: Duration = Duration::from_secs(60);
const BURST: usize = 20;

#[derive(Debug, Default)]
pub struct RejectLimiter {
    last: HashMap<(EndpointId, Vec<u8>), Instant>,
    window: Option<Instant>,
    in_window: usize,
}

impl RejectLimiter {
    /// Whether this reject is reported.
    pub fn allow(&mut self, remote: EndpointId, alpn: &[u8], now: Instant) -> bool {
        if self.last.len() > 4096 {
            self.last.retain(|_, t| now.duration_since(*t) < PER_KEY);
        }
        let key = (remote, alpn.to_vec());
        if let Some(t) = self.last.get(&key) {
            if now.duration_since(*t) < PER_KEY {
                return false;
            }
        }
        match self.window {
            Some(w) if now.duration_since(w) < WINDOW => {
                if self.in_window >= BURST {
                    return false;
                }
            }
            _ => {
                self.window = Some(now);
                self.in_window = 0;
            }
        }
        self.in_window += 1;
        self.last.insert(key, now);
        true
    }
}

/// The policy in force plus where rejects are reported. Shared by the hook
/// and the control loop.
#[derive(Debug)]
pub struct Gate {
    pub policy: RwLock<Policy>,
    rejects: Mutex<RejectLimiter>,
    events: Events,
}

impl Gate {
    pub fn new(policy: Policy, events: Events) -> Arc<Self> {
        Arc::new(Gate {
            policy: RwLock::new(policy),
            rejects: Mutex::new(RejectLimiter::default()),
            events,
        })
    }

    pub fn admits(&self, alpn: &[u8], remote: &EndpointId) -> bool {
        self.policy.read().unwrap().admits(alpn, remote)
    }

    fn rejected(&self, remote: EndpointId, alpn: &[u8]) {
        if self
            .rejects
            .lock()
            .unwrap()
            .allow(remote, alpn, Instant::now())
        {
            self.events.try_emit(Event {
                endpoint: hex::encode(remote.as_bytes()),
                alpn: String::from_utf8_lossy(alpn).into_owned(),
                ..Event::new("reject")
            });
        }
    }
}

/// The endpoint hook (server side only).
#[derive(Debug, Clone)]
pub struct Hook(pub Arc<Gate>);

impl EndpointHooks for Hook {
    async fn after_handshake<'a>(&'a self, conn: &'a Connection) -> AfterHandshakeOutcome {
        let alpn = conn.alpn();
        let remote = conn.remote_id();
        if self.0.admits(alpn, &remote) {
            return AfterHandshakeOutcome::Accept;
        }
        tracing::debug!(%remote, alpn = %String::from_utf8_lossy(alpn), "refused");
        self.0.rejected(remote, alpn);
        AfterHandshakeOutcome::Reject {
            error_code: VarInt::from_u32(REFUSED),
            reason: b"not allowed".to_vec(),
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use iroh::SecretKey;

    fn id(n: u8) -> EndpointId {
        SecretKey::from_bytes(&[n; 32]).public()
    }

    #[test]
    fn policy() {
        let mut p = Policy::default();
        let (dev, other, probe) = (id(1), id(2), id(3));
        p.allow.insert(dev);
        assert!(p.admits(ALPN_TUNNEL_NAME, &dev));
        assert!(!p.admits(ALPN_TUNNEL_NAME, &other), "not on the allowlist");
        assert!(!p.admits(ALPN_PAIR_NAME, &dev), "pairing closed");
        p.pairing = true;
        assert!(
            p.admits(ALPN_PAIR_NAME, &other),
            "pairing open: anyone may try"
        );
        assert!(!p.admits(ALPN_SELFTEST_NAME, &probe));
        p.selftest.insert(probe);
        assert!(p.admits(ALPN_SELFTEST_NAME, &probe));
        assert!(
            !p.admits(ALPN_SELFTEST_NAME, &dev),
            "selftest only for the probe"
        );
        assert!(!p.admits(b"DUMBPIPEV0", &dev), "unknown ALPN");
        assert!(!p.admits(b"", &dev));
    }

    #[test]
    fn rejects_are_rate_limited() {
        let mut l = RejectLimiter::default();
        let t0 = Instant::now();
        assert!(l.allow(id(1), ALPN_TUNNEL_NAME, t0));
        assert!(
            !l.allow(id(1), ALPN_TUNNEL_NAME, t0 + Duration::from_secs(1)),
            "same key within a minute"
        );
        assert!(l.allow(id(1), ALPN_PAIR_NAME, t0), "another ALPN");
        assert!(
            l.allow(id(1), ALPN_TUNNEL_NAME, t0 + PER_KEY),
            "a minute later"
        );
        // the overall burst
        let mut l = RejectLimiter::default();
        let n = (0..100u8)
            .filter(|i| l.allow(id(*i), ALPN_TUNNEL_NAME, t0))
            .count();
        assert_eq!(n, BURST);
        assert!(
            l.allow(id(200), ALPN_TUNNEL_NAME, t0 + WINDOW),
            "a new window"
        );
    }
}
