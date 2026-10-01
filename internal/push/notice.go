package push

// Notice sends one of xbind's own notifications to user — the partition
// mode acts (plans/partitions/01 §2.4-§2.5): a switch request to a tile's
// managers (kind tile.partition-switch), a wipe notice to a person whose
// partition was deleted (tile.partition-deleted). The kinds sit under
// "tile", so an app that registered for tile notifications gets them;
// link is workspace-relative (c/<tile>/…). It spends the person's budget
// like a tile's notification (dropped quietly over it) and ignores a
// tile's mute: these are governance notices about the person's data.
// Nothing is sent while push is off, or to a disabled or unknown account.
func (s *Service) Notice(user, kind, title, body, link, collapse string) {
	if s == nil || user == "" || !s.Enabled() {
		return
	}
	if _, ok := s.account(user); !ok {
		return
	}
	n := note{user: user, kind: kind, title: title, body: body, link: link}
	if collapse != "" {
		n.collapse = "xbin:" + collapse
	}
	posts := s.posts(user, n.kind)
	if posts == 0 {
		return
	}
	if ok, _ := s.user.allowN(user, posts); !ok {
		s.snd.limited.Add(1)
		return
	}
	s.enqueue(n)
}
