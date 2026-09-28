package agentcore

// ExitRootGone is `bx __sbx-agent`'s exit code when the sandbox's root
// filesystem died under it: the fuse-overlayfs it watches (--fuse-pid,
// sandbox.Spec.FuseWatch) exited. The runtime says "the sandbox's root
// filesystem (fuse-overlayfs) died" (plans/tile-sandbox-runtime.md §2.4, §7).
// Its other codes: 0 when xbind closed the factory, 1 for a setup or
// transport failure, 2 when it isn't PID 1.
const ExitRootGone = 3
