package broker

// personpage.go — GET /partitions' features name the partitions page
// (/xbin/partitions, internal/server/personpage.go; plans/partitions 06
// §12.1): a client that links people to it — the shell's paused card, the
// app — asks for this word rather than guess from a version (an xbind
// without it answers the page 404).

// PartitionsPageFeature is the partitions page's word in features.
const PartitionsPageFeature = "partitions-page/1"

func init() { registerPartitionFeature(PartitionsPageFeature) }
