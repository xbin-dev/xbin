package broker

// builtins_set.go — the builtin catalogs boot installs (moved out of
// broker.go, unchanged).

import "github.com/xbin-dev/xbin/internal/builtins"

// SetBuiltins installs the embedded builtin tile catalog (from main, which
// owns the embedded FS). Call before Register.
func (b *Broker) SetBuiltins(s *builtins.Set) { b.tiles = s }

// SetBuiltinTemplates installs the embedded builtin template catalog. Call
// before Register.
func (b *Broker) SetBuiltinTemplates(s *builtins.TemplateSet) { b.templates = s }

// SetUpdater installs the builtin update tracker (plans/builtin-updates.md).
// Call before Register.
func (b *Broker) SetUpdater(u *builtins.Updater) { b.updater = u }
