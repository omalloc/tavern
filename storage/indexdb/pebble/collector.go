package pebble

import (
	"github.com/prometheus/client_golang/prometheus"
)

var _ prometheus.Collector = (*PebbleDB)(nil)

const (
	pebbleNamespace = "tavern"
	pebbleSubsystem = "pebble"
)

type pebbleCollectorDescs struct {
	memtableSize           *prometheus.Desc
	memtableCount          *prometheus.Desc
	memtableZombieSize     *prometheus.Desc
	memtableZombieCount    *prometheus.Desc
	walSize                *prometheus.Desc
	walPhysicalSize        *prometheus.Desc
	walObsoletePhysical    *prometheus.Desc
	walFiles               *prometheus.Desc
	walObsoleteFiles       *prometheus.Desc
	walBytesWritten        *prometheus.Desc
	walBytesIn             *prometheus.Desc
	tableLiveSize          *prometheus.Desc
	tableLiveCount         *prometheus.Desc
	tableObsoleteSize      *prometheus.Desc
	tableObsoleteCount     *prometheus.Desc
	tableZombieSize        *prometheus.Desc
	tableZombieCount       *prometheus.Desc
	tableBackingCount      *prometheus.Desc
	tableBackingSize       *prometheus.Desc
	blobLiveSize           *prometheus.Desc
	blobLiveCount          *prometheus.Desc
	blobObsoleteSize       *prometheus.Desc
	blobObsoleteCount      *prometheus.Desc
	blobZombieSize         *prometheus.Desc
	blobZombieCount        *prometheus.Desc
	uptimeSeconds          *prometheus.Desc
	tableIterators         *prometheus.Desc
	compactInProgress      *prometheus.Desc
	compactInProgressBytes *prometheus.Desc
	compactEstimatedDebt   *prometheus.Desc
	flushInProgress        *prometheus.Desc
	all                    []*prometheus.Desc
}

var defaultPebbleCollectorDescs = newPebbleCollectorDescs()

func newPebbleCollectorDescs() *pebbleCollectorDescs {
	build := func(name, help string) *prometheus.Desc {
		return prometheus.NewDesc(prometheus.BuildFQName(pebbleNamespace, pebbleSubsystem, name), help, []string{"db"}, nil)
	}

	d := &pebbleCollectorDescs{
		memtableSize:           build("memtable_size_bytes", "Allocated bytes in all memtables"),
		memtableCount:          build("memtable_count", "Current memtable count"),
		memtableZombieSize:     build("memtable_zombie_size_bytes", "Bytes held by zombie memtables"),
		memtableZombieCount:    build("memtable_zombie_count", "Zombie memtable count"),
		walSize:                build("wal_logical_size_bytes", "Logical size of live WAL data"),
		walPhysicalSize:        build("wal_physical_size_bytes", "Physical size of WAL files"),
		walObsoletePhysical:    build("wal_obsolete_physical_size_bytes", "Physical size of obsolete WAL files"),
		walFiles:               build("wal_files", "Number of live WAL files"),
		walObsoleteFiles:       build("wal_obsolete_files", "Number of obsolete WAL files"),
		walBytesWritten:        build("wal_bytes_written_total", "Total bytes written to WAL"),
		walBytesIn:             build("wal_bytes_in_total", "Logical bytes received by WAL"),
		tableLiveSize:          build("table_live_size_bytes", "Size of live SSTables (local)"),
		tableLiveCount:         build("table_live_count", "Count of live SSTables (local)"),
		tableObsoleteSize:      build("table_obsolete_size_bytes", "Size of obsolete SSTables"),
		tableObsoleteCount:     build("table_obsolete_count", "Count of obsolete SSTables"),
		tableZombieSize:        build("table_zombie_size_bytes", "Size of zombie SSTables"),
		tableZombieCount:       build("table_zombie_count", "Count of zombie SSTables"),
		tableBackingCount:      build("table_backing_count", "Count of physical SSTables backing virtual tables"),
		tableBackingSize:       build("table_backing_size_bytes", "Size of physical SSTables backing virtual tables"),
		blobLiveSize:           build("blob_live_size_bytes", "Physical size of live blob files"),
		blobLiveCount:          build("blob_live_count", "Count of live blob files"),
		blobObsoleteSize:       build("blob_obsolete_size_bytes", "Physical size of obsolete blob files"),
		blobObsoleteCount:      build("blob_obsolete_count", "Count of obsolete blob files"),
		blobZombieSize:         build("blob_zombie_size_bytes", "Physical size of zombie blob files"),
		blobZombieCount:        build("blob_zombie_count", "Count of zombie blob files"),
		uptimeSeconds:          build("uptime_seconds", "DB uptime in seconds"),
		tableIterators:         build("table_iterators", "Number of open SSTable iterators"),
		compactInProgress:      build("compaction_in_progress", "Compactions currently running"),
		compactInProgressBytes: build("compaction_in_progress_bytes", "Bytes involved in in-progress compactions"),
		compactEstimatedDebt:   build("compaction_estimated_debt_bytes", "Estimated compaction debt"),
		flushInProgress:        build("flush_in_progress", "Flush operations currently running"),
	}

	d.all = []*prometheus.Desc{
		d.memtableSize,
		d.memtableCount,
		d.memtableZombieSize,
		d.memtableZombieCount,
		d.walSize,
		d.walPhysicalSize,
		d.walObsoletePhysical,
		d.walFiles,
		d.walObsoleteFiles,
		d.walBytesWritten,
		d.walBytesIn,
		d.tableLiveSize,
		d.tableLiveCount,
		d.tableObsoleteSize,
		d.tableObsoleteCount,
		d.tableZombieSize,
		d.tableZombieCount,
		d.tableBackingCount,
		d.tableBackingSize,
		d.blobLiveSize,
		d.blobLiveCount,
		d.blobObsoleteSize,
		d.blobObsoleteCount,
		d.blobZombieSize,
		d.blobZombieCount,
		d.uptimeSeconds,
		d.tableIterators,
		d.compactInProgress,
		d.compactInProgressBytes,
		d.compactEstimatedDebt,
		d.flushInProgress,
	}

	return d
}

func (d *pebbleCollectorDescs) describe(ch chan<- *prometheus.Desc) {
	for _, desc := range d.all {
		ch <- desc
	}
}

// RegisterMetrics registers the Pebble collector on the default registry.
func (p *PebbleDB) RegisterMetrics() {
	p.registerOnce.Do(func() {
		prometheus.MustRegister(p)
	})
}

// Collect implements [prometheus.Collector].
func (p *PebbleDB) Collect(ch chan<- prometheus.Metric) {
	if p == nil || p.db == nil || p.descs == nil {
		return
	}

	m := p.db.Metrics()
	label := p.dbLabel

	ch <- prometheus.MustNewConstMetric(p.descs.memtableSize, prometheus.GaugeValue, float64(m.MemTable.Size), label)
	ch <- prometheus.MustNewConstMetric(p.descs.memtableCount, prometheus.GaugeValue, float64(m.MemTable.Count), label)
	ch <- prometheus.MustNewConstMetric(p.descs.memtableZombieSize, prometheus.GaugeValue, float64(m.MemTable.ZombieSize), label)
	ch <- prometheus.MustNewConstMetric(p.descs.memtableZombieCount, prometheus.GaugeValue, float64(m.MemTable.ZombieCount), label)

	ch <- prometheus.MustNewConstMetric(p.descs.walSize, prometheus.GaugeValue, float64(m.WAL.Size), label)
	ch <- prometheus.MustNewConstMetric(p.descs.walPhysicalSize, prometheus.GaugeValue, float64(m.WAL.PhysicalSize), label)
	ch <- prometheus.MustNewConstMetric(p.descs.walObsoletePhysical, prometheus.GaugeValue, float64(m.WAL.ObsoletePhysicalSize), label)
	ch <- prometheus.MustNewConstMetric(p.descs.walFiles, prometheus.GaugeValue, float64(m.WAL.Files), label)
	ch <- prometheus.MustNewConstMetric(p.descs.walObsoleteFiles, prometheus.GaugeValue, float64(m.WAL.ObsoleteFiles), label)
	ch <- prometheus.MustNewConstMetric(p.descs.walBytesWritten, prometheus.CounterValue, float64(m.WAL.BytesWritten), label)
	ch <- prometheus.MustNewConstMetric(p.descs.walBytesIn, prometheus.CounterValue, float64(m.WAL.BytesIn), label)

	ch <- prometheus.MustNewConstMetric(p.descs.tableLiveSize, prometheus.GaugeValue, float64(m.Table.Local.LiveSize), label)
	ch <- prometheus.MustNewConstMetric(p.descs.tableLiveCount, prometheus.GaugeValue, float64(m.Table.Local.LiveCount), label)
	ch <- prometheus.MustNewConstMetric(p.descs.tableObsoleteSize, prometheus.GaugeValue, float64(m.Table.ObsoleteSize), label)
	ch <- prometheus.MustNewConstMetric(p.descs.tableObsoleteCount, prometheus.GaugeValue, float64(m.Table.ObsoleteCount), label)
	ch <- prometheus.MustNewConstMetric(p.descs.tableZombieSize, prometheus.GaugeValue, float64(m.Table.ZombieSize), label)
	ch <- prometheus.MustNewConstMetric(p.descs.tableZombieCount, prometheus.GaugeValue, float64(m.Table.ZombieCount), label)
	ch <- prometheus.MustNewConstMetric(p.descs.tableBackingCount, prometheus.GaugeValue, float64(m.Table.BackingTableCount), label)
	ch <- prometheus.MustNewConstMetric(p.descs.tableBackingSize, prometheus.GaugeValue, float64(m.Table.BackingTableSize), label)

	ch <- prometheus.MustNewConstMetric(p.descs.blobLiveSize, prometheus.GaugeValue, float64(m.BlobFiles.LiveSize), label)
	ch <- prometheus.MustNewConstMetric(p.descs.blobLiveCount, prometheus.GaugeValue, float64(m.BlobFiles.LiveCount), label)
	ch <- prometheus.MustNewConstMetric(p.descs.blobObsoleteSize, prometheus.GaugeValue, float64(m.BlobFiles.ObsoleteSize), label)
	ch <- prometheus.MustNewConstMetric(p.descs.blobObsoleteCount, prometheus.GaugeValue, float64(m.BlobFiles.ObsoleteCount), label)
	ch <- prometheus.MustNewConstMetric(p.descs.blobZombieSize, prometheus.GaugeValue, float64(m.BlobFiles.ZombieSize), label)
	ch <- prometheus.MustNewConstMetric(p.descs.blobZombieCount, prometheus.GaugeValue, float64(m.BlobFiles.ZombieCount), label)

	ch <- prometheus.MustNewConstMetric(p.descs.tableIterators, prometheus.GaugeValue, float64(m.TableIters), label)
	ch <- prometheus.MustNewConstMetric(p.descs.compactInProgress, prometheus.GaugeValue, float64(m.Compact.NumInProgress), label)
	ch <- prometheus.MustNewConstMetric(p.descs.compactInProgressBytes, prometheus.GaugeValue, float64(m.Compact.InProgressBytes), label)
	ch <- prometheus.MustNewConstMetric(p.descs.compactEstimatedDebt, prometheus.GaugeValue, float64(m.Compact.EstimatedDebt), label)
	ch <- prometheus.MustNewConstMetric(p.descs.flushInProgress, prometheus.GaugeValue, float64(m.Flush.NumInProgress), label)

	ch <- prometheus.MustNewConstMetric(p.descs.uptimeSeconds, prometheus.GaugeValue, m.Uptime.Seconds(), label)
}

// Describe implements [prometheus.Collector].
func (p *PebbleDB) Describe(ch chan<- *prometheus.Desc) {
	if p == nil || p.descs == nil {
		return
	}
	p.descs.describe(ch)
}
