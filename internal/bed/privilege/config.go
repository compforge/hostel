package privilege

// Config supplies a preferred fixed identity. Without one, the
// domain selects shared or dedicated identities from the profile and host facts.
type Config struct {
	UID        int
	GID        int
	Configured bool
}
type Options struct {
	UID *int
	GID *int
}
