package agent

import "github.com/xbin-dev/xbin/sdk/acp"

// maxNameBytes is the attachment name limit the tests here were written
// against, from before attachments moved to sdk/acp.
const maxNameBytes = acp.MaxNameBytes
