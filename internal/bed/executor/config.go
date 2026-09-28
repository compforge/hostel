package executor

// PrivatePIDNamespace requires each Executor to own a Linux PID namespace and
// matching procfs. It is opt-in; unsupported hosts must reject the requirement.
type Config struct {
	Backend             string
	PrivatePIDNamespace bool
}
type Options struct {
	Backend             *string
	PrivatePIDNamespace *bool
}
