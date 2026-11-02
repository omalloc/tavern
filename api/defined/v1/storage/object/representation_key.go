package object

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"net/url"
	"strings"
)

const representationKeyPrefix = "#tavern-v2/"

var ErrInvalidResourceKey = errors.New("invalid representation resource key")

// ResourceKey is immutable. Isolation scope participates in the digest without
// exposing the configured scope in physical suffixes.
type ResourceKey struct {
	canonicalURL string
	namespace    string
	resourceID   string
}

func NewResourceKey(rawURL, namespace, isolationScope string) (ResourceKey, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u == nil || u.Host == "" || u.User != nil || namespace == "" {
		return ResourceKey{}, ErrInvalidResourceKey
	}
	u.Scheme = strings.ToLower(u.Scheme)
	if u.Scheme != "http" && u.Scheme != "https" {
		return ResourceKey{}, ErrInvalidResourceKey
	}
	u.Host = strings.ToLower(u.Host)
	u.Fragment, u.RawFragment = "", ""
	if u.Path == "" {
		u.Path = "/"
	}
	canonical := u.String() // retain RawPath, RawQuery, duplicate parameter order and ForceQuery
	return ResourceKey{canonical, namespace, structuredDigest("resource", namespace, canonical, isolationScope)}, nil
}

func (k ResourceKey) Valid() bool {
	return k.canonicalURL != "" && k.namespace != "" && validDigest(k.resourceID)
}
func (k ResourceKey) CanonicalURL() string { return k.canonicalURL }
func (k ResourceKey) Namespace() string    { return k.namespace }
func (k ResourceKey) ResourceID() string   { return k.resourceID }

// ResourceKeyFromRecord restores an opaque key from trusted control metadata;
// the original isolation scope need not be persisted or disclosed.
func ResourceKeyFromRecord(record *ResourceRecord) (ResourceKey, error) {
	if record == nil || !validDigest(record.ResourceID) {
		return ResourceKey{}, ErrInvalidResourceKey
	}
	parsed, err := NewResourceKey(record.CanonicalURL, record.Namespace, "")
	if err != nil || parsed.CanonicalURL() != record.CanonicalURL {
		return ResourceKey{}, ErrInvalidResourceKey
	}
	parsed.resourceID = record.ResourceID
	return parsed, nil
}

func ResourceControlID(canonicalURL, resourceID string) (*ID, error) {
	if !validDigest(resourceID) {
		return nil, ErrInvalidResourceKey
	}
	parsed, err := NewResourceKey(canonicalURL, "control", "")
	if err != nil || parsed.CanonicalURL() != canonicalURL {
		return nil, ErrInvalidResourceKey
	}
	return NewVirtualID(canonicalURL, representationKeyPrefix+string(RecordResource)+"/"+resourceID), nil
}
func (k ResourceKey) ID() *ID {
	if !k.Valid() {
		return nil
	}
	return NewVirtualID(k.canonicalURL, representationKeyPrefix+string(RecordResource)+"/"+k.resourceID)
}

func (k ResourceKey) RepresentationID(incarnation string, purgeEpoch uint64, selectorFingerprint, bodyGeneration string) (*ID, error) {
	if !k.Valid() || incarnation == "" || bodyGeneration == "" || !validDigest(selectorFingerprint) {
		return nil, ErrInvalidResourceKey
	}
	var epoch [8]byte
	binary.BigEndian.PutUint64(epoch[:], purgeEpoch)
	digest := structuredDigest("representation", k.resourceID, incarnation, string(epoch[:]), selectorFingerprint, bodyGeneration)
	return NewVirtualID(k.canonicalURL, representationKeyPrefix+string(RecordRepresentation)+"/"+digest), nil
}

func structuredDigest(kind string, parts ...string) string {
	h := sha256.New()
	for _, part := range append([]string{"tavern-representation-v2", kind}, parts...) {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(part)))
		h.Write(length[:])
		h.Write([]byte(part))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func validDigest(digest string) bool {
	if len(digest) != sha256.Size*2 {
		return false
	}
	for _, c := range digest {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

func representationKeyKind(ext string) RecordKind {
	if !strings.HasPrefix(ext, representationKeyPrefix) {
		return ""
	}
	kind, digest, ok := strings.Cut(strings.TrimPrefix(ext, representationKeyPrefix), "/")
	if !ok || !validDigest(digest) {
		return ""
	}
	if kind != string(RecordResource) && kind != string(RecordRepresentation) && kind != string(RecordBarriers) {
		return ""
	}
	return RecordKind(kind)
}

func (id *ID) IsControl() bool {
	return id != nil && (representationKeyKind(id.ext) == RecordResource || representationKeyKind(id.ext) == RecordBarriers)
}

func (id *ID) IsBarrierControl() bool {
	return id != nil && representationKeyKind(id.ext) == RecordBarriers
}
func (id *ID) IsRepresentation() bool {
	return id != nil && representationKeyKind(id.ext) == RecordRepresentation
}
