package deployments

// WorkTreeMoved is the watcher's notice that a batch touched tile while its
// live reload is paused: the plane recounts how far the work tree moved from
// the pinned checkpoint, debounced per tile, and announces a moved count. It
// is never called for a tile whose live reload is attached.
func (p *Plane) WorkTreeMoved(tile string) {}
