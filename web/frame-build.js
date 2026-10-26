// frame-build.js — what a <bx-frame>'s tile code is doing (D196): the
// runner's build events for the page it shows, into the frame's build
// state (live | building | failed) and, while building, its phase —
// 'compile' (the code is being built), 'env' (its environment layer is
// being set up: a build-start with text), 'starting' (built; the backend
// is starting and waited for). The frame announces a change as `bx-build`
// {state, phase}; the shell's title bar and sidebar draw it.

// setBuild(f, phase | null, error?): a build event for f's page — phase
// while building, null when it ended (error: why it failed, or null).
// f.buildSeen says one reached the frame since it was made: after it, what
// the frame says beats an older /components row.
export function setBuild(f, phase, error = null) {
  f._buildSeen = true;
  if (phase) { f._building = true; f._phase = phase; f._buildError = null; return; }
  f._building = false; f._phase = ''; f._buildError = error;
}

// onBuildEvent(f, e) → whether e was a build event of f's primary page (or
// of the person's own partition of it: a `partitions` op state, D196), now
// applied.
export function onBuildEvent(f, e) {
  if (e.component !== f.src || f.deployment) return false;
  let ev = e.type, text = e.text;
  if (ev === 'partitions') {
    if (e.data?.op !== 'state') return false;
    ev = e.data.event; text = e.data.text;
  }
  switch (ev) {
    case 'build-start': setBuild(f, text ? 'env' : 'compile'); return true;
    case 'backend-starting': setBuild(f, 'starting'); return true;
    case 'build-error': setBuild(f, null, text || 'build failed'); return true;
    case 'build-ok': setBuild(f, null); return true;
  }
  return false;
}
