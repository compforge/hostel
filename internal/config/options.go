package config

import (
	"time"

	"github.com/qiankunli/hostel/internal/bed/configuration"
	"github.com/qiankunli/hostel/internal/bed/executor"
	"github.com/qiankunli/hostel/internal/bed/filesystem"
	"github.com/qiankunli/hostel/internal/bed/network"
	"github.com/qiankunli/hostel/internal/bed/privilege"
	"github.com/qiankunli/hostel/internal/bed/resource"
	"github.com/qiankunli/hostel/internal/bed/store"
)

type BedConfig struct {
	Configuration configuration.Config
	RoomType      string
	Filesystem    filesystem.Config
	Network       network.Config
	Resource      resource.Config
	Privilege     privilege.Config
	Executor      executor.Config
	Store         store.Config
}
type BedOptions struct {
	ConfigurationSources *map[string]string
	RoomType             *string
	Filesystem           filesystem.Options
	Network              network.Options
	Resource             resource.Options
	Privilege            privilege.Options
	Executor             executor.Options
	Store                store.Options
}

// Options contains only explicitly supplied values. Pointer presence, not the
// value's zero-ness, wins over flags/environment/defaults. Runtime Config is concrete.
// +spec=`Explicit options override CLI/env/defaults, including Auto, false and zero; components never read startup environment.`
type Options struct {
	PortRangeStart              *int
	PortRangeEnd                *int
	Bed                         BedOptions
	ShowVersion                 *bool
	HealthCheck                 *bool
	EnableTracing               *bool
	OTLPTracesGRPCEndpoint      *string
	OTLPTracesHTTPEndpoint      *string
	Addr                        *string
	BedsRoot                    *string
	DefaultBed                  *string
	BedIdleTTL                  *time.Duration
	MaxBeds                     *int
	MaxPinnedBeds               *int
	BedPressureThresholdPercent *int
	PersistInterval             *time.Duration
	LuggageHighBytes            *int64
	LuggageLowBytes             *int64
	ChromiumPath                *string
	ChromiumCDPURL              *string
	ChromiumIdleStop            *time.Duration
	ChromiumDebugPort           *int
	ShellPath                   *string
}

func (o Options) apply(c *Config) {
	apply(&c.Bed.Configuration.Sources, o.Bed.ConfigurationSources)
	apply(&c.Bed.RoomType, o.Bed.RoomType)
	apply(&c.PortRangeStart, o.PortRangeStart)
	apply(&c.PortRangeEnd, o.PortRangeEnd)
	apply(&c.ShowVersion, o.ShowVersion)
	apply(&c.HealthCheck, o.HealthCheck)
	apply(&c.EnableTracing, o.EnableTracing)
	apply(&c.OTLPTracesGRPCEndpoint, o.OTLPTracesGRPCEndpoint)
	apply(&c.OTLPTracesHTTPEndpoint, o.OTLPTracesHTTPEndpoint)
	apply(&c.Addr, o.Addr)
	apply(&c.BedsRoot, o.BedsRoot)
	apply(&c.Bed.Filesystem.DormReadFallbackRoot, o.Bed.Filesystem.DormReadFallbackRoot)
	apply(&c.DefaultBed, o.DefaultBed)
	apply(&c.BedIdleTTL, o.BedIdleTTL)
	apply(&c.MaxBeds, o.MaxBeds)
	apply(&c.MaxPinnedBeds, o.MaxPinnedBeds)
	apply(&c.BedPressureThresholdPercent, o.BedPressureThresholdPercent)
	apply(&c.PersistInterval, o.PersistInterval)
	apply(&c.LuggageHighBytes, o.LuggageHighBytes)
	apply(&c.LuggageLowBytes, o.LuggageLowBytes)
	apply(&c.ChromiumPath, o.ChromiumPath)
	apply(&c.ChromiumCDPURL, o.ChromiumCDPURL)
	apply(&c.ChromiumIdleStop, o.ChromiumIdleStop)
	apply(&c.ChromiumDebugPort, o.ChromiumDebugPort)
	apply(&c.ShellPath, o.ShellPath)
	apply(&c.Bed.Filesystem.Bwrap, o.Bed.Filesystem.Bwrap)
	apply(&c.Bed.Filesystem.Landlock, o.Bed.Filesystem.Landlock)
	apply(&c.Bed.Filesystem.UID, o.Bed.Filesystem.UID)
	apply(&c.Bed.Filesystem.PRoot, o.Bed.Filesystem.PRoot)
	apply(&c.Bed.Filesystem.Pathshim, o.Bed.Filesystem.Pathshim)
	apply(&c.Bed.Network.NetNS, o.Bed.Network.NetNS)
	apply(&c.Bed.Resource.Cgroup, o.Bed.Resource.Cgroup)
	apply(&c.Bed.Resource.Admission.CPUPressureThresholdPercent, o.Bed.Resource.CPUPressureThresholdPercent)
	apply(&c.Bed.Resource.Admission.MemoryPressureThresholdPercent, o.Bed.Resource.MemoryPressureThresholdPercent)
	apply(&c.Bed.Privilege.UID, o.Bed.Privilege.UID)
	apply(&c.Bed.Privilege.GID, o.Bed.Privilege.GID)
	apply(&c.Bed.Executor.Backend, o.Bed.Executor.Backend)
	apply(&c.Bed.Store.Sync, o.Bed.Store.Sync)
	apply(&c.Bed.Store.ResticBinary, o.Bed.Store.ResticBinary)
	apply(&c.Bed.Store.ResticPassword, o.Bed.Store.ResticPassword)
	apply(&c.Bed.Store.Bucket, o.Bed.Store.Bucket)
	apply(&c.Bed.Store.Prefix, o.Bed.Store.Prefix)
	apply(&c.Bed.Store.Endpoint, o.Bed.Store.Endpoint)
	apply(&c.Bed.Store.PathStyle, o.Bed.Store.PathStyle)
	apply(&c.Bed.Store.Region, o.Bed.Store.Region)
	apply(&c.Bed.Store.AccessKeyID, o.Bed.Store.AccessKeyID)
	apply(&c.Bed.Store.SecretAccessKey, o.Bed.Store.SecretAccessKey)
	apply(&c.Bed.Store.SessionToken, o.Bed.Store.SessionToken)
	apply(&c.Bed.Store.AutoPackFileThreshold, o.Bed.Store.AutoPackFileThreshold)
	apply(&c.Bed.Resource.Admission.CPUThresholdPercent, o.Bed.Resource.CPUThresholdPercent)
	apply(&c.Bed.Resource.Admission.MemoryThresholdPercent, o.Bed.Resource.MemoryThresholdPercent)
}

func apply[T any](dst *T, explicit *T) {
	if explicit != nil {
		*dst = *explicit
	}
}
