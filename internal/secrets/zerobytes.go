package secrets

// ZeroBytes overwrites the slice with zeroes to minimise the window
// during which sensitive plaintext sits in process memory.
func ZeroBytes(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
