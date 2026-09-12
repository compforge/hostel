package privilege

// Config selects the fixed identity; UID isolation still allocates dedicated users.
type Config struct {
	UID int
	GID int
}
type Options struct {
	UID *int
	GID *int
}
