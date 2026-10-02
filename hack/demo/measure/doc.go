// Package measure takes the numbers the promo film and the website may
// claim, end to end against real isolated xbinds built from this tree, and
// writes every sample to disk (hack/demo/measurements.md has the method and
// the results).
//
// The tests carry the build tag xbinmeasure, so no other run builds them:
//
//	hack/demo/measure/run.sh            # every measurement
//	hack/demo/measure/run.sh vm         # one (vm, save, swap, partition, browser)
//
// They need what `make integration`'s isolated suites need (user
// namespaces, the base rootfs, the helpers in .dev.mk) plus /dev/kvm for
// the VM measurements and Playwright (PLAYWRIGHT_DIR) for the browser
// ones; run them with the Bash tool's sandbox off on a dev box.
package measure
