package representation

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestWritebackManifestUpgradeRequiresOriginalBarrierIdentity(t *testing.T) {
	bindings := map[string]controlBinding{"bucket": {Identity: "pebble/index", Incarnation: "purge-history"}}
	for _, tc := range []struct {
		name     string
		version  int
		controls map[string]controlBinding
		valid    bool
	}{
		{"v2", 2, nil, true},
		{"v3", 3, bindings, true},
		{"v3-missing", 3, nil, false},
		{"v3-replaced", 3, map[string]controlBinding{"bucket": {Identity: "pebble/index", Incarnation: "new-history"}}, false},
		{"v4-missing", 4, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "representation-topology.json")
			data, _ := json.Marshal(topologyManifest{Version: tc.version, Fingerprint: "topology", Controls: tc.controls})
			if err := os.WriteFile(path, data, 0600); err != nil {
				t.Fatal(err)
			}
			err := checkTopologyManifest(dir, "topology", bindings, true)
			if !tc.valid {
				if !errors.Is(err, ErrTopologyMismatch) {
					t.Fatal("unbound invalidation history was accepted", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err = os.ReadFile(path)
			var upgraded topologyManifest
			if err != nil || json.Unmarshal(data, &upgraded) != nil || upgraded.Version != 4 || upgraded.Controls["bucket"] != bindings["bucket"] {
				t.Fatal("writeback upgrade lost the original control binding", upgraded, err)
			}
		})
	}
}
