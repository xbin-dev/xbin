//! The control plane (internal/apptunnel/wire, native/spec/tunnel.md): one
//! JSON object per line, xbind → sidecar on stdin (the first line is
//! `init`, carrying the secret key), sidecar → xbind on stdout. The field
//! names, order and omissions mirror Go's `wire` package exactly, so an
//! event serializes to the bytes Go would write (testdata/wire-vectors.json).

use serde::{Deserialize, Serialize};

/// The relay configuration.
#[derive(Debug, Clone, Default, PartialEq, Eq, Deserialize, Serialize)]
pub struct Relay {
    /// "n0" | "custom" | "disabled" (tests: direct only).
    pub mode: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub url: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub token: String,
}

/// The first stdin line.
#[derive(Debug, Clone, Deserialize)]
pub struct Init {
    pub t: String,
    pub v: u32,
    #[serde(rename = "secretKey")]
    pub secret_key: String,
    pub relay: Relay,
    /// Go writes a nil slice as `null`.
    #[serde(default)]
    pub allow: Option<Vec<String>>,
    #[serde(default)]
    pub pairing: bool,
}

/// Every later stdin line.
#[derive(Debug, Clone, Default, Deserialize)]
pub struct Command {
    pub t: String,
    #[serde(default)]
    pub seq: u64,
    /// Omitted when empty (Go's omitempty): an `allow` without it is the empty list.
    #[serde(default)]
    pub allow: Option<Vec<String>>,
    #[serde(default)]
    pub open: bool,
    #[serde(default)]
    pub endpoint: String,
}

fn zero_u64(v: &u64) -> bool {
    *v == 0
}
fn zero_i64(v: &i64) -> bool {
    *v == 0
}
fn is_false(v: &bool) -> bool {
    !*v
}

/// One stdout line. Fields in Go's order, each omitted when Go omits it.
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct Event {
    pub t: String,
    #[serde(
        rename = "endpointId",
        default,
        skip_serializing_if = "String::is_empty"
    )]
    pub endpoint_id: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub ticket: String,
    #[serde(
        rename = "homeRelay",
        default,
        skip_serializing_if = "String::is_empty"
    )]
    pub home_relay: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub version: String,
    #[serde(default, skip_serializing_if = "zero_u64")]
    pub seq: u64,
    #[serde(rename = "relayConnected", default, skip_serializing_if = "is_false")]
    pub relay_connected: bool,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub conns: Vec<ConnInfo>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub endpoint: String,
    #[serde(default, skip_serializing_if = "zero_u64")]
    pub conn: u64,
    #[serde(default, skip_serializing_if = "is_false")]
    pub open: bool,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub path: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub alpn: String,
    #[serde(default, skip_serializing_if = "is_false")]
    pub reachable: bool,
    #[serde(rename = "rttMs", default, skip_serializing_if = "zero_i64")]
    pub rtt_ms: i64,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub error: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub msg: String,
    #[serde(default, skip_serializing_if = "is_false")]
    pub fatal: bool,
}

/// One live connection in a status event (every field always present).
#[derive(Debug, Clone, Default, PartialEq, Eq, Serialize, Deserialize)]
pub struct ConnInfo {
    pub endpoint: String,
    pub conn: u64,
    pub path: String,
    #[serde(rename = "rttMs")]
    pub rtt_ms: i64,
}

impl Event {
    pub fn new(t: &str) -> Self {
        Event {
            t: t.to_string(),
            ..Default::default()
        }
    }

    pub fn error(msg: impl Into<String>, fatal: bool) -> Self {
        Event {
            t: "error".into(),
            msg: msg.into(),
            fatal,
            ..Default::default()
        }
    }

    /// The line, without its newline.
    pub fn line(&self) -> String {
        serde_json::to_string(self).expect("an event always serializes")
    }
}

/// Parses the first stdin line.
pub fn parse_init(line: &str) -> Result<Init, String> {
    let init: Init = serde_json::from_str(line).map_err(|e| format!("init: {e}"))?;
    if init.t != "init" {
        return Err(format!("init: the first line is {:?}, not init", init.t));
    }
    if init.v != 1 {
        return Err(format!("init: version {} (this sidecar speaks 1)", init.v));
    }
    Ok(init)
}

/// Parses a later stdin line.
pub fn parse_command(line: &str) -> Result<Command, String> {
    serde_json::from_str(line).map_err(|e| format!("command: {e}"))
}

#[cfg(test)]
pub(crate) mod tests {
    use super::*;

    #[derive(Deserialize)]
    pub(crate) struct Vectors {
        pub headers: Vec<HeaderVector>,
        pub init: Vec<String>,
        pub commands: Vec<String>,
        pub events: Vec<String>,
    }

    #[derive(Deserialize)]
    pub(crate) struct HeaderVector {
        pub alpn: u8,
        pub endpoint: String,
        pub conn: u64,
        pub bytes: String,
    }

    /// The fixture Go writes (internal/apptunnel/wire/vectors_test.go).
    pub(crate) fn vectors() -> Vectors {
        serde_json::from_str(include_str!("../testdata/wire-vectors.json")).unwrap()
    }

    const ID: &str = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f";

    #[test]
    fn init_lines() {
        let v = vectors();
        let a = parse_init(&v.init[0]).unwrap();
        assert_eq!(a.secret_key, "a1".repeat(32));
        assert_eq!(
            a.relay,
            Relay {
                mode: "custom".into(),
                url: "https://relay.example.com".into(),
                token: "tok-123".into()
            }
        );
        assert_eq!(a.allow.unwrap(), vec![ID.to_string()]);
        assert!(a.pairing);
        let b = parse_init(&v.init[1]).unwrap(); // allow: null
        assert_eq!(b.relay.mode, "disabled");
        assert!(b.allow.unwrap_or_default().is_empty());
        assert!(!b.pairing);
        let c = parse_init(&v.init[2]).unwrap();
        assert_eq!(c.relay.mode, "n0");
        assert!(c.allow.unwrap().is_empty());
        assert!(
            parse_init(&v.commands[0]).is_err(),
            "a command is not an init"
        );
        assert!(parse_init(r#"{"t":"init","v":2,"secretKey":"","relay":{"mode":"n0"}}"#).is_err());
        assert!(parse_init("not json").is_err());
    }

    #[test]
    fn command_lines() {
        let v = vectors();
        let c: Vec<Command> = v
            .commands
            .iter()
            .map(|l| parse_command(l).unwrap())
            .collect();
        assert_eq!((c[0].t.as_str(), c[0].seq), ("allow", 7));
        assert_eq!(c[0].allow.clone().unwrap(), vec![ID.to_string()]);
        assert_eq!((c[1].t.as_str(), c[1].seq), ("allow", 8));
        assert!(
            c[1].allow.is_none(),
            "an empty allow list is omitted, and means empty"
        );
        assert!(c[2].open && c[2].t == "pairing");
        assert!(!c[3].open && c[3].t == "pairing");
        assert_eq!((c[4].t.as_str(), c[4].endpoint.as_str()), ("close", ID));
        assert_eq!(c[5].t, "status");
        assert_eq!(c[6].t, "selftest");
        // unknown fields are ignored (a newer xbind may add some)
        assert_eq!(
            parse_command(r#"{"t":"status","later":1}"#).unwrap().t,
            "status"
        );
    }

    /// Events serialize to exactly the bytes Go writes for the same values.
    #[test]
    fn event_lines() {
        let v = vectors();
        let conn = |n, path: &str, rtt| ConnInfo {
            endpoint: ID.into(),
            conn: n,
            path: path.into(),
            rtt_ms: rtt,
        };
        let want = vec![
            Event {
                endpoint_id: ID.into(),
                ticket: "endpointabc".into(),
                home_relay: "https://relay.example.com/".into(),
                version: "0.1.0".into(),
                ..Event::new("ready")
            },
            Event {
                seq: 7,
                ..Event::new("ack")
            },
            Event {
                home_relay: "https://relay.example.com/".into(),
                relay_connected: true,
                conns: vec![conn(3, "direct", 12), conn(4, "relay", 0)],
                ..Event::new("status")
            },
            Event::new("status"),
            Event {
                endpoint: ID.into(),
                conn: 3,
                open: true,
                path: "relay".into(),
                alpn: "xbin/tunnel/1".into(),
                ..Event::new("conn")
            },
            Event {
                endpoint: ID.into(),
                conn: 3,
                ..Event::new("conn")
            },
            Event {
                endpoint: ID.into(),
                alpn: "xbin/pair/1".into(),
                ..Event::new("reject")
            },
            Event {
                reachable: true,
                path: "relay".into(),
                rtt_ms: 40,
                ..Event::new("selftest")
            },
            Event {
                error: "timed out".into(),
                ..Event::new("selftest")
            },
            Event::error("bind: address in use", true),
        ];
        assert_eq!(want.len(), v.events.len());
        for (e, line) in want.iter().zip(v.events.iter()) {
            assert_eq!(&e.line(), line);
            let back: Event = serde_json::from_str(line).unwrap();
            assert_eq!(&back, e);
        }
    }
}
