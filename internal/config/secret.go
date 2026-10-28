package config

// Secret holds a sensitive value resolved by Load.
type Secret struct {
	value []byte
}

// Value returns the secret bytes.
func (s Secret) Value() []byte {
	return s.value
}
