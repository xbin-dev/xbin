#!/bin/sh
# hack/ui-harness/quietnet.sh — what Playwright starts as "the browser" (see
# quietnet.js): a network namespace of its own, the loopback relay, then the
# real browser with the same arguments, pipes and pid.
#
# The veth pair is only there so the browser reads as online, as it does on
# the host (a namespace with nothing but a loopback is navigator.onLine ===
# false); it carries nothing and, with no IPv6 link-local address to settle
# a second later (addrgenmode none), never changes once the browser is up.
exec unshare -Urn -- sh -c '
  ip link set lo up || exit 70
  ip link add qn0 type veth peer name qn1 2>/dev/null && ip link set qn0 addrgenmode none && ip link set qn1 addrgenmode none &&
    ip addr add 192.0.2.1/24 dev qn0 && ip link set qn1 up && ip link set qn0 up
  QUIETNET_PARENT=$$ "$QUIETNET_NODE" "$QUIETNET_JS" inner &
  i=0
  while [ ! -e "$QUIETNET_DIR/ready" ] && [ "$i" -lt 500 ]; do sleep 0.01; i=$((i + 1)); done
  exec "$QUIETNET_EXE" "$@"' quietnet "$@"
