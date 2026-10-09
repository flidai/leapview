package encryption

// ActiveWriteKeyID identifies the selected encryption key without exposing key
// material. Rotation keeps all decrypt-only keys in the operator-owned keyring.
func (k *Keyring) ActiveWriteKeyID() string {
	if k == nil {
		return ""
	}
	return k.activeWriteKey
}
