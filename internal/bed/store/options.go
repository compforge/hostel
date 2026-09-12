package store

// Options holds explicit daemon startup overrides; Config contains resolved values.
type Options struct {
	PersistedPaths        *[]string
	Sync                  *string
	ResticBinary          *string
	ResticPassword        *string
	Bucket                *string
	Prefix                *string
	Endpoint              *string
	PathStyle             *bool
	Region                *string
	AccessKeyID           *string
	SecretAccessKey       *string
	SessionToken          *string
	AutoPackFileThreshold *int
}
