import Foundation

// LocalAuthentication as Shared/AgentNoticeStore.swift uses it (D241 review
// finding 2: whether the device has a passcode).
public enum LAPolicy: Int, Sendable { case deviceOwnerAuthenticationWithBiometrics = 1, deviceOwnerAuthentication = 2 }
open class LAContext: NSObject {
    public override init() {}
    open func canEvaluatePolicy(_ policy: LAPolicy, error: UnsafeMutablePointer<NSError?>?) -> Bool { true }
}
