package filesystem

// IdentityRoots owns credential-bound file handles for one resident Bed.
// Closing it terminates the worker after all Pillow views have been released.
type IdentityRoots interface {
	BindRoot(Root) (Root, error)
	Close() error
}
