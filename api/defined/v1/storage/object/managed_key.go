package object

// TransformFingerprint binds opaque normalized specifications to both policy
// and encoder versions. The transform package owns semantic normalization.
func TransformFingerprint(spec []byte, policy, encoder string) string {
	return structuredDigest("transform", string(spec), policy, encoder)
}

func ManagedSelectorFingerprint(source *ID, version, transform string) (string, error) {
	if !source.IsRepresentation() || version == "" || !validDigest(transform) {
		return "", ErrInvalidResourceKey
	}
	return structuredDigest("managed", source.Path(), source.Ext(), version, transform), nil
}
