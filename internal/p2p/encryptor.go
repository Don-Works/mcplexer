package p2p

// Encryptor is the minimal interface NewHost needs to encrypt the persistent
// libp2p identity key at rest. *secrets.AgeEncryptor satisfies this
// implicitly. Pass nil to fall back to cleartext storage (tests, dev).
type Encryptor interface {
	Encrypt(plaintext []byte) ([]byte, error)
	Decrypt(ciphertext []byte) ([]byte, error)
}
