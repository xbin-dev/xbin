package users

// NormalizeID is an account id as the store keys it: two spellings name the
// same account exactly when these are equal (D246: an access.set line
// compares ids as the store does, never by Unicode case folding).
func NormalizeID(id string) string { return normalizeID(id) }

// LevelRank orders the access levels: none (or unknown) 0, read 1, write 2,
// terminal 3.
func LevelRank(l string) int { return levelRank(l) }
