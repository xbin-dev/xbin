import Foundation

/// Why adding or signing in to a workspace failed, told apart the way the
/// person has to act on it (the add-workspace screens show ``title`` and
/// ``message``). Every onboarding path — the QR code, the address, an
/// invite, SSO — turns its error into one of these with
/// ``init(_:server:)``, whatever layer threw it: the transport (URL
/// errors), the enrollment and sign-in routes (``Enrollment/Failure``,
/// ``SignInError``, ``APIError``), or a body that isn't what an xbin
/// workspace sends.
///
/// Foundation only: URL errors are read through their `NSError` bridge
/// (domain `NSURLErrorDomain` and its stable codes), so this compiles and
/// is tested on Linux, where `URLError` lives in FoundationNetworking.
public enum ConnectProblem: Error, Sendable, Equatable, CustomStringConvertible {
    /// DNS, refused, timed out, offline: nothing answered at `host`.
    case cantConnect(host: String, detail: String)
    /// The TLS handshake failed: an untrusted, expired or not-yet-valid
    /// certificate, or no TLS where the address says https.
    case certificate(host: String, detail: String)
    /// Something answered, but not as an xbin workspace does — or as one
    /// too old for what was asked (the app's routes are missing).
    case notXbin(host: String, detail: String)
    /// The enrollment code was refused: expired, used, or wrong.
    case codeRefused(detail: String)
    /// The account can't sign in: wrong password, disabled, SSO only, an
    /// invalid invite, a password the workspace's policy refuses, the
    /// device limit. The text says which.
    case account(String)
    /// 429: too many failed attempts from this address.
    case throttled
    /// 5xx.
    case server(status: Int, message: String)
    /// Anything else, as it came.
    case other(String)

    /// The class, for tests and for choosing an icon.
    public enum Kind: String, Sendable, Equatable {
        case cantConnect, certificate, notXbin, codeRefused, account, throttled, server, other
    }

    public var kind: Kind {
        switch self {
        case .cantConnect: return .cantConnect
        case .certificate: return .certificate
        case .notXbin: return .notXbin
        case .codeRefused: return .codeRefused
        case .account: return .account
        case .throttled: return .throttled
        case .server: return .server
        case .other: return .other
        }
    }

    /// A few words: the headline of the error card.
    public var title: String {
        switch self {
        case .cantConnect: return "Can't connect"
        case .certificate: return "Certificate problem"
        case .notXbin: return "Not an xbin workspace"
        case .codeRefused: return "Code refused"
        case .account: return "Can't sign in"
        case .throttled: return "Too many attempts"
        case .server: return "Server error"
        case .other: return "Something went wrong"
        }
    }

    /// What happened and what to do about it.
    public var message: String {
        switch self {
        case .cantConnect(let host, let detail):
            return "This phone can't reach \(host)\(Self.paren(detail)). Is it on the same network as the workspace, "
                + "or does it need a VPN? If your browser reaches xbin through a tunnel or a proxy, set "
                + "\"address your phone uses\" in the workspace's add-device panel (settings → add a device) "
                + "to an address this phone can reach."
        case .certificate(let host, let detail):
            return "\(host)'s certificate isn't one this phone trusts\(Self.paren(detail)): it may be self-signed, "
                + "expired, or for another name. Use the workspace's address that has a valid certificate."
        case .notXbin(let host, let detail):
            return "\(host) didn't answer like an xbin workspace\(Self.paren(detail)). Check the address. If it is "
                + "your workspace, its xbin may be older than this app needs: its operator can update it."
        case .codeRefused(let detail):
            return "The workspace didn't accept the code\(Self.paren(detail)). A code works once and only for "
                + "5 minutes. Make a new one in the workspace: settings → add a device."
        case .account(let m):
            return m
        case .throttled:
            return "Too many failed attempts from this network. Wait half a minute, then try again."
        case .server(let status, let m):
            return "The workspace had a problem (HTTP \(status))\(Self.paren(m)). Try again; if it keeps "
                + "happening, tell its operator."
        case .other(let m):
            return m
        }
    }

    public var description: String { "\(title): \(message)" }

    private static func paren(_ s: String) -> String {
        let t = s.trimmingCharacters(in: .whitespacesAndNewlines)
        return t.isEmpty ? "" : " (\(t))"
    }

    // MARK: Classifying

    /// Classifies any error from the onboarding paths; `server` names the
    /// workspace in the messages (its `host[:port]`).
    public init(_ error: any Error, server: ServerOrigin?) {
        let host = server?.authority ?? "the workspace"
        switch error {
        case let p as ConnectProblem:
            self = p
        case let f as Enrollment.Failure:
            self = Self.enrollment(f, host: host)
        case let s as SignInError:
            self = Self.signIn(s, host: host)
        case let a as APIError:
            self = Self.api(a, host: host)
        case let i as ServerOrigin.Invalid:
            self = .other("That isn't a workspace address: \(i.reason).")
        case let l as SignInLink.Invalid:
            self = .other(l.description)
        case is JSONParseError, is DeviceLogin.Invalid:
            self = .notXbin(host: host, detail: "its answer isn't what an xbin workspace sends")
        default:
            self = Self.transport(error, host: host) ?? .other(Self.text(error))
        }
    }

    /// A transport failure (URLSession's `NSURLErrorDomain`, or a POSIX
    /// socket error) as a problem; nil when `error` is neither.
    public static func transport(_ error: any Error, host: String) -> ConnectProblem? {
        let ns = error as NSError
        if ns.domain == urlErrorDomain {
            if let d = unreachable[ns.code] { return .cantConnect(host: host, detail: d) }
            if let d = tls[ns.code] { return .certificate(host: host, detail: d) }
            switch ns.code {
            case -1011, -1017, -1015, -1016:
                // badServerResponse, cannotParseResponse, cannotDecode…: an
                // https address on a plain-HTTP port answers like this too.
                return .notXbin(host: host, detail: "its answer isn't HTTP this app understands")
            case -999:
                return .other("Cancelled.")
            default:
                return .other(text(error))
            }
        }
        if ns.domain == NSPOSIXErrorDomain, let d = posixUnreachable[ns.code] {
            return .cantConnect(host: host, detail: d)
        }
        return nil
    }

    /// `NSURLErrorDomain` (a literal: the constant is FoundationNetworking's
    /// on Linux).
    public static let urlErrorDomain = "NSURLErrorDomain"

    /// Nothing answered.
    static let unreachable: [Int: String] = [
        -1001: "timed out",                 // timedOut
        -1003: "no such host",              // cannotFindHost
        -1004: "connection refused",        // cannotConnectToHost
        -1005: "the connection was lost",   // networkConnectionLost
        -1006: "no such host",              // dnsLookupFailed
        -1009: "this phone is offline",     // notConnectedToInternet
        -1018: "roaming is off",            // internationalRoamingOff
        -1019: "a call is active",          // callIsActive
        -1020: "mobile data is off",        // dataNotAllowed
    ]

    /// The TLS handshake.
    static let tls: [Int: String] = [
        -1200: "the secure connection failed",          // secureConnectionFailed
        -1201: "the certificate has expired",           // serverCertificateHasBadDate
        -1202: "the certificate isn't trusted",         // serverCertificateUntrusted
        -1203: "the certificate's issuer is unknown",   // serverCertificateHasUnknownRoot
        -1204: "the certificate isn't valid yet",       // serverCertificateNotYetValid
        -1205: "the server refused this device's certificate", // clientCertificateRejected
        -1206: "the server wants a client certificate", // clientCertificateRequired
        -1022: "this address needs https",              // appTransportSecurityRequiresSecureConnection
    ]

    /// ECONNREFUSED, ETIMEDOUT, EHOSTUNREACH, ENETUNREACH, ENETDOWN, EHOSTDOWN
    /// — Linux and Darwin numbers.
    static let posixUnreachable: [Int: String] = {
        var m: [Int: String] = [:]
        for (codes, d) in [([111, 61], "connection refused"), ([110, 60], "timed out"),
                           ([113, 65], "no route to the host"), ([101, 51], "no route to the network"),
                           ([100, 50], "the network is down"), ([112, 64], "the host is down")] {
            for c in codes { m[c] = d }
        }
        return m
    }()

    static func enrollment(_ f: Enrollment.Failure, host: String) -> ConnectProblem {
        switch f {
        case .badCode: return .codeRefused(detail: "it isn't a code: 26 letters and digits")
        case .codeRefused(let m): return .codeRefused(detail: m)
        case .badKey(let m): return .other("The workspace refused this device's key (\(m)).")
        case .deviceLimit(let m): return .account("Can't add this device: \(m)")
        case .stepUp, .notAUser, .invalidCredentials, .inviteInvalid, .passwordRejected:
            return .account(f.description)
        case .noAccounts: return .account(f.description)
        case .inviteNeedsBrowser:
            return .notXbin(host: host, detail: "its xbin is older than app invites; open the link in a browser")
        case .server(let e): return api(e, host: host)
        }
    }

    static func signIn(_ s: SignInError, host: String) -> ConnectProblem {
        switch s {
        case .throttled: return .throttled
        case .cancelled: return .other(s.description)
        case .server(let e): return api(e, host: host)
        case .keyUnavailable: return .other(s.description)
        default: return .account(s.description)
        }
    }

    /// `xbin://sso?error=<code>`: the codes the web login page explains
    /// (internal/server/sso.go ssoErrText), in the same words.
    public static func sso(error code: String) -> ConnectProblem {
        switch code {
        case "denied": return .account("Sign-in was cancelled at the identity provider.")
        case "noaccount":
            return .account("No account is bound to that identity, and its domain isn't on the workspace's allow-list. Ask a workspace admin.")
        case "disabled": return .account("That account is disabled. Ask a workspace admin.")
        case "failed": return .account("Single sign-on failed. Try again.")
        default: return .account("Single sign-on failed (\(code)).")
        }
    }

    /// A non-2xx answer from a route the app needs.
    static func api(_ e: APIError, host: String) -> ConnectProblem {
        switch e.status {
        case 429: return .throttled
        case 500...599: return .server(status: e.status, message: e.message)
        case 404: return .notXbin(host: host, detail: "it has no \(e.body == nil ? "such page" : "such route")")
        case 300...399: return .notXbin(host: host, detail: "it answered with a redirect")
        case 401, 403: return .account(e.message)
        default: return .other(e.description)
        }
    }

    /// An error's own words: an NSError's localized description (its
    /// `description` is domain and code), else what it prints.
    static func text(_ error: any Error) -> String {
        if type(of: error) is NSError.Type { return (error as NSError).localizedDescription }
        return String(describing: error)
    }
}
