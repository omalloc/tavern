package representation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/gofrs/flock"

	"github.com/omalloc/tavern/api/defined/v1/storage"
	"github.com/omalloc/tavern/api/defined/v1/storage/object"
	pkgmetrics "github.com/omalloc/tavern/pkg/metrics"
)

var ErrTopologyMismatch = errors.New("representation topology changed; explicit offline migration or rebuild is required")

type Provider interface{ RepresentationManager() *Manager }

type ManagerConfig struct {
	Maintenance    func(context.Context) error `json:"-" yaml:"-"`
	StateDirectory string                      `json:"state_directory" yaml:"state_directory"`
	Durable        bool                        `json:"durable" yaml:"durable"`
	GCInterval     time.Duration               `json:"gc_interval" yaml:"gc_interval"`
	GCBudget       int                         `json:"gc_budget" yaml:"gc_budget"`
}

type Manager struct {
	buckets     []storage.Bucket
	repos       map[string]*IndexRepository
	fingerprint string
	gcMu        sync.Mutex
	cursors     map[string][]byte
	nextBucket  int
	compactNext int
	stop        chan struct{}
	closed      sync.Once
	wg          sync.WaitGroup
	stateLock   *flock.Flock
}

var _ storage.RepresentationPurger = (*Manager)(nil)

// NewManager binds stable control ownership to a fixed topology. Health hints
// never participate in rendezvous hashing. A durable state directory anchors
// the topology even if all bucket members are replaced in configuration.
func NewManager(ctx context.Context, buckets []storage.Bucket, config ManagerConfig) (*Manager, error) {
	if len(buckets) == 0 || config.GCInterval < 0 || config.GCBudget < 0 {
		return nil, ErrInvalidRecord
	}
	m := &Manager{buckets: append([]storage.Bucket(nil), buckets...), repos: make(map[string]*IndexRepository), cursors: make(map[string][]byte), stop: make(chan struct{})}
	success := false
	defer func() {
		if !success && m.stateLock != nil {
			m.stateLock.Close()
		}
	}()
	for _, bucket := range m.buckets {
		if bucket == nil {
			return nil, ErrInvalidRecord
		}
	}
	sort.Slice(m.buckets, func(i, j int) bool { return m.buckets[i].ID() < m.buckets[j].ID() })
	topology := make([]string, 0, len(buckets))
	for i, bucket := range m.buckets {
		if bucket == nil || bucket.ID() == "" || i > 0 && bucket.ID() == m.buckets[i-1].ID() {
			return nil, ErrInvalidRecord
		}
		if _, ok := bucket.(storage.ControlStore); !ok {
			return nil, storage.ErrControlUnsupported
		}
		if _, ok := bucket.(storage.MetadataScanner); !ok {
			return nil, storage.ErrControlUnsupported
		}
		if _, ok := bucket.(storage.RepresentationEviction); !ok {
			return nil, storage.ErrControlUnsupported
		}
		topology = append(topology, bucket.ID()+"\x00"+bucket.Type())
	}
	encoded, _ := json.Marshal(struct {
		Version int
		Durable bool
		Buckets []string
	}{2, config.Durable, topology})
	digest := sha256.Sum256(encoded)
	m.fingerprint = hex.EncodeToString(digest[:])
	if config.Durable {
		if config.StateDirectory == "" {
			return nil, ErrTopologyMismatch
		}
		if err := os.MkdirAll(config.StateDirectory, 0700); err != nil {
			return nil, err
		}
		m.stateLock = flock.New(filepath.Join(config.StateDirectory, ".tavern-writer.lock"))
		locked, err := m.stateLock.TryLock()
		if err != nil {
			return nil, err
		}
		if !locked {
			return nil, fmt.Errorf("representation state directory already has a writer")
		}
		if err := checkTopologyManifest(config.StateDirectory, m.fingerprint, false); err != nil {
			return nil, err
		}
	}
	for _, bucket := range m.buckets {
		opts := []Option{WithLifecycle(m.buckets...)}
		if config.Durable {
			opts = append(opts, WithDurability(m.buckets...))
		}
		repo, err := NewIndexRepository(bucket.(storage.ControlStore), bucket.ID(), func(ctx context.Context, location string, id *object.ID) (*object.Metadata, error) {
			for _, data := range m.buckets {
				if data.ID() == location {
					return data.Lookup(ctx, id)
				}
			}
			return nil, storage.ErrKeyNotFound
		}, opts...)
		if err != nil {
			return nil, err
		}
		m.repos[bucket.ID()] = repo
		catalog, err := repo.loadCatalog(ctx)
		if err != nil {
			return nil, err
		}
		if catalog.Barriers.TopologyFingerprint != "" && catalog.Barriers.TopologyFingerprint != m.fingerprint {
			return nil, ErrTopologyMismatch
		}
		if catalog.Barriers.TopologyFingerprint == "" && len(m.buckets) > 1 {
			if len(catalog.Barriers.Entries) > 0 {
				return nil, ErrTopologyMismatch
			}
			var cursor []byte
			for {
				records, next, err := bucket.(storage.MetadataScanner).ScanMetadata(ctx, cursor, 128)
				if err != nil {
					return nil, err
				}
				for _, md := range records {
					if md.RecordKind == object.RecordResource || md.RecordKind == object.RecordRepresentation {
						return nil, ErrTopologyMismatch
					}
				}
				if next == nil {
					break
				}
				cursor = next
			}
		}
	}
	if config.Durable {
		if err := checkTopologyManifest(config.StateDirectory, m.fingerprint, true); err != nil {
			return nil, err
		}
	}
	for _, bucket := range m.buckets {
		repo := m.repos[bucket.ID()]
		catalog, err := repo.loadCatalog(ctx)
		if err != nil {
			return nil, err
		}
		if catalog.Barriers.TopologyFingerprint == "" {
			catalog.Barriers.TopologyFingerprint = m.fingerprint
			if err := repo.storeControl(ctx, catalog); err != nil {
				return nil, err
			}
		}
	}
	for _, bucket := range m.buckets {
		location := bucket.ID()
		bucket.(storage.RepresentationEviction).SetRepresentationEvictor(func(ctx context.Context, md *object.Metadata) error {
			if md == nil || md.Representation == nil {
				return ErrInvalidRecord
			}
			repo, err := m.repositoryForID(md.Representation.ResourceID)
			if err != nil {
				return err
			}
			return repo.EvictReference(ctx, location, md)
		})
	}
	if err := m.Recover(ctx); err != nil {
		m.Close()
		return nil, err
	}
	if config.GCInterval > 0 {
		budget := config.GCBudget
		if budget == 0 {
			budget = 64
		}
		m.wg.Go(func() {
			ticker := time.NewTicker(config.GCInterval)
			defer ticker.Stop()
			for {
				select {
				case <-m.stop:
					return
				case <-ticker.C:
					ctx, cancel := context.WithTimeout(context.Background(), config.GCInterval)
					_, _ = m.CollectUnreachable(ctx, budget)
					_, _ = m.CompactBarriers(ctx, min(budget, 128))
					if config.Maintenance != nil {
						_ = config.Maintenance(ctx)
					}
					cancel()
				}
			}
		})
	}
	success = true
	return m, nil
}

func (m *Manager) HomeID(resourceID string) (string, error) {
	if len(resourceID) != 64 {
		return "", ErrInvalidRecord
	}
	if _, err := hex.DecodeString(resourceID); err != nil {
		return "", ErrInvalidRecord
	}
	var best []byte
	var owner string
	for _, bucket := range m.buckets {
		score := sha256.Sum256([]byte("tavern-control-owner-v2\x00" + resourceID + "\x00" + bucket.ID()))
		if owner == "" || bytes.Compare(score[:], best) > 0 {
			owner = bucket.ID()
			best = append(best[:0], score[:]...)
		}
	}
	return owner, nil
}

func (m *Manager) repositoryForID(resourceID string) (*IndexRepository, error) {
	id, err := m.HomeID(resourceID)
	if err != nil {
		return nil, err
	}
	return m.repos[id], nil
}
func (m *Manager) Repository(key object.ResourceKey) (*IndexRepository, error) {
	if !key.Valid() {
		return nil, ErrInvalidRecord
	}
	return m.repositoryForID(key.ResourceID())
}

func (m *Manager) Purge(ctx context.Context, raw string, control storage.PurgeControl) (failure error) {
	defer func() { pkgmetrics.RecordRepresentationInvalidation(control.Hard, control.Dir, failure != nil) }()
	var failures []error
	for _, bucket := range m.buckets {
		if err := m.repos[bucket.ID()].Purge(ctx, raw, control); err != nil {
			failures = append(failures, err)
		}
	}
	return errors.Join(failures...)
}

func (m *Manager) Close() error {
	m.closed.Do(func() {
		close(m.stop)
		m.wg.Wait()
		for _, bucket := range m.buckets {
			bucket.(storage.RepresentationEviction).SetRepresentationEvictor(nil)
		}
		if m.stateLock != nil {
			m.stateLock.Close()
			m.stateLock = nil
		}
	})
	return nil
}

func checkTopologyManifest(directory, fingerprint string, create bool) error {
	path := filepath.Join(directory, "representation-topology.json")
	data, err := os.ReadFile(path)
	if err == nil {
		var manifest struct {
			Version     int    `json:"version"`
			Fingerprint string `json:"fingerprint"`
		}
		if json.Unmarshal(data, &manifest) != nil || manifest.Version != 2 || manifest.Fingerprint != fingerprint {
			return ErrTopologyMismatch
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if !create {
		return nil
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	data, _ = json.Marshal(struct {
		Version     int    `json:"version"`
		Fingerprint string `json:"fingerprint"`
	}{2, fingerprint})
	file, err := os.CreateTemp(directory, ".topology-*")
	if err != nil {
		return err
	}
	name := file.Name()
	defer os.Remove(name)
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(name, path); err != nil {
		return err
	}
	for _, dir := range []string{directory, filepath.Dir(filepath.Clean(directory))} {
		file, err := os.Open(dir)
		if err != nil {
			return err
		}
		err = file.Sync()
		closeErr := file.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}

func (m *Manager) Recover(ctx context.Context) error {
	for _, bucket := range m.buckets {
		var cursor []byte
		for {
			records, next, err := bucket.(storage.MetadataScanner).ScanMetadata(ctx, cursor, 128)
			if err != nil {
				return err
			}
			for _, md := range records {
				if md.RecordKind != object.RecordResource || md.Resource == nil || md.Resource.SchemaVersion != object.RepresentationSchemaVersion {
					continue
				}
				key, err := object.ResourceKeyFromRecord(md.Resource)
				if err != nil {
					return err
				}
				home, _ := m.HomeID(key.ResourceID())
				if home != bucket.ID() {
					return ErrTopologyMismatch
				}
				repo := m.repos[home]
				state, err := repo.Capture(ctx, key)
				if err != nil {
					return err
				}
				root := state.Resource()
				if repo.buckets[root.Resource.ActiveLocation] == nil {
					return ErrTopologyMismatch
				}
				for _, ref := range root.Resource.Variants {
					if validateReference(ref) != nil {
						return ErrInvalidRecord
					}
					child, err := repo.lookup(ctx, root.Resource.ActiveLocation, ref.RepresentationID)
					if err == nil && child.Representation != nil && child.Representation.SchemaVersion != object.RepresentationSchemaVersion {
						continue
					}
					valid := err == nil && validChild(key, root.Resource, ref, child)
					if valid {
						err = repo.verifyAt(ctx, root.Resource.ActiveLocation, child)
						valid = err == nil
					}
					if !valid {
						if err != nil && !errors.Is(err, storage.ErrKeyNotFound) && !errors.Is(err, os.ErrNotExist) {
							return err
						}
						if _, err := repo.Retire(ctx, key, ref); err != nil && !errors.Is(err, ErrSuperseded) {
							return err
						}
						continue
					}
					repo.buckets[root.Resource.ActiveLocation].Touch(ctx, child.ID)
				}
			}
			if next == nil {
				break
			}
			cursor = next
		}
	}
	return nil
}

type CollectionResult struct{ Scanned, Collected, Retained, Protected int }

func (m *Manager) CollectUnreachable(ctx context.Context, budget int) (CollectionResult, error) {
	var result CollectionResult
	if budget <= 0 {
		pkgmetrics.RecordRepresentationGC("failed")
		return result, ErrInvalidRecord
	}
	m.gcMu.Lock()
	defer m.gcMu.Unlock()
	visitedBuckets := 0
	for result.Scanned < budget && visitedBuckets < len(m.buckets) {
		bucket := m.buckets[m.nextBucket]
		remaining := min(128, budget-result.Scanned)
		records, next, err := bucket.(storage.MetadataScanner).ScanMetadata(ctx, m.cursors[bucket.ID()], remaining)
		if err != nil {
			pkgmetrics.RecordRepresentationGC("failed")
			return result, err
		}
		m.cursors[bucket.ID()] = next
		for _, md := range records {
			result.Scanned++
			if md.RecordKind != object.RecordRepresentation || md.Representation == nil || md.Representation.SchemaVersion != object.RepresentationSchemaVersion {
				result.Protected++
				pkgmetrics.RecordRepresentationGC("skipped")
				continue
			}
			repo, err := m.repositoryForID(md.Representation.ResourceID)
			if err != nil {
				result.Protected++
				pkgmetrics.RecordRepresentationGC("skipped")
				continue
			}
			collected, err := repo.collectCandidate(ctx, bucket.ID(), md)
			if errors.Is(err, ErrInvalidRecord) || errors.Is(err, ErrUnsupportedSchema) {
				result.Protected++
				pkgmetrics.RecordRepresentationGC("skipped")
				continue
			}
			if err != nil {
				pkgmetrics.RecordRepresentationGC("failed")
				return result, fmt.Errorf("representation collection: %w", err)
			}
			if collected {
				result.Collected++
				pkgmetrics.RecordRepresentationGC("collected")
			} else {
				result.Retained++
				pkgmetrics.RecordRepresentationGC("skipped")
			}
		}
		if next == nil {
			m.nextBucket = (m.nextBucket + 1) % len(m.buckets)
			visitedBuckets++
		}
	}
	return result, nil
}
