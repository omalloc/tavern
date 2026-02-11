package storage

import "time"

type (
	PromoteConfig struct {
		MinHits int           `json:"min_hits" yaml:"min_hits"` // 时间窗口内命中 >= N
		Window  time.Duration `json:"window" yaml:"window"`     // 时间窗口 1m
	}
	DemoteConfig struct {
		MinHits   int           `json:"min_hits" yaml:"min_hits"`   // 时间窗口内命中 <= N
		Window    time.Duration `json:"window" yaml:"window"`       // 时间窗口 1m
		Occupancy float64       `json:"occupancy" yaml:"occupancy"` // 热盘存储占用率 >= N%
	}
	MigrationConfig struct {
		Enabled bool          `json:"enabled" yaml:"enabled"`
		Promote PromoteConfig `json:"promote" yaml:"promote"` // 升温
		Demote  DemoteConfig  `json:"demote" yaml:"demote"`   // 降温
	}

	BucketConfig struct {
		Path           string           `json:"path" yaml:"path"`                         // local path or ?
		Driver         string           `json:"driver" yaml:"driver"`                     // native, custom-driver
		Type           string           `json:"type" yaml:"type"`                         // normal, cold, hot, fastmemory
		DBType         string           `json:"db_type" yaml:"db_type"`                   // boltdb, badgerdb, pebble
		DBPath         string           `json:"db_path" yaml:"db_path"`                   // db path, defult: <bucket_path>/.indexdb
		AsyncLoad      bool             `json:"async_load" yaml:"async_load"`             // load metadata async
		SliceSize      uint64           `json:"slice_size" yaml:"slice_size"`             // slice size for each part
		MaxObjectLimit int              `json:"max_object_limit" yaml:"max_object_limit"` // max object limit, upper Bound discard
		Migration      *MigrationConfig `json:"migration" yaml:"migration"`               // migration config
		DBConfig       map[string]any   `json:"db_config" yaml:"db_config"`               // custom db config
	}
)
