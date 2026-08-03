// exectool is the exec probe for the container-store integration tests: a
// tiny static binary the suites install into gocryptfs/overlay mounts and
// then execute. Exec succeeding and printing this marker is the assertion.
package main

import "fmt"

func main() {
	fmt.Println("EXEC-OK")
}
