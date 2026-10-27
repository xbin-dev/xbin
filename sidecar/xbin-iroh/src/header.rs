//! The stream header (internal/apptunnel/wire): written in cleartext at the
//! start of every stream handed to xbind, before the splice.
//!
//! ```text
//! 0  4  magic "XBI1"    4 1 version=1    5 1 alpn: 1=xbin/tunnel/1, 2=xbin/pair/1
//! 6  2  reserved=0      8 32 remote EndpointId    40 8 connection serial (BE)
//! ```

pub const HEADER_LEN: usize = 48;
pub const MAGIC: [u8; 4] = *b"XBI1";
pub const VERSION: u8 = 1;

pub const ALPN_TUNNEL: u8 = 1;
pub const ALPN_PAIR: u8 = 2;

pub const ALPN_TUNNEL_NAME: &[u8] = b"xbin/tunnel/1";
pub const ALPN_PAIR_NAME: &[u8] = b"xbin/pair/1";
/// The sidecar's own: its selftest dials itself on this ALPN from a
/// throwaway endpoint, which the hook admits by id. Never handed to xbind.
pub const ALPN_SELFTEST_NAME: &[u8] = b"xbin/selftest/1";

/// The header code of an ALPN that xbind is handed, if it is one.
pub fn alpn_code(alpn: &[u8]) -> Option<u8> {
    match alpn {
        ALPN_TUNNEL_NAME => Some(ALPN_TUNNEL),
        ALPN_PAIR_NAME => Some(ALPN_PAIR),
        _ => None,
    }
}

/// The 48 header bytes.
pub fn encode(alpn: u8, endpoint: &[u8; 32], conn: u64) -> [u8; HEADER_LEN] {
    let mut b = [0u8; HEADER_LEN];
    b[0..4].copy_from_slice(&MAGIC);
    b[4] = VERSION;
    b[5] = alpn;
    b[8..40].copy_from_slice(endpoint);
    b[40..48].copy_from_slice(&conn.to_be_bytes());
    b
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::control::tests::vectors;

    #[test]
    fn matches_go() {
        let v = vectors();
        assert!(!v.headers.is_empty());
        for h in v.headers {
            let ep: [u8; 32] = hex::decode(&h.endpoint).unwrap().try_into().unwrap();
            assert_eq!(hex::encode(encode(h.alpn, &ep, h.conn)), h.bytes);
        }
    }

    #[test]
    fn alpns() {
        assert_eq!(alpn_code(b"xbin/tunnel/1"), Some(1));
        assert_eq!(alpn_code(b"xbin/pair/1"), Some(2));
        assert_eq!(alpn_code(ALPN_SELFTEST_NAME), None);
        assert_eq!(alpn_code(b"xbin/tunnel/2"), None);
    }
}
