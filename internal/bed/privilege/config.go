package privilege

// Config supplies an explicit fixed-identity requirement. Without one, the
// domain selects shared or dedicated identities from the profile and host facts.
type Config struct {
	UID      int
	GID      int
	Explicit bool
}
type Options struct {
	UID *int
	GID *int
}
