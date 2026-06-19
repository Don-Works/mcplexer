package oauth

import (
	"encoding/json"
	"fmt"

	"github.com/don-works/mcplexer/internal/store"
)

func (fm *FlowManager) decryptTokenData(data []byte) (*store.OAuthTokenData, error) {
	if fm.encryptor == nil {
		return nil, fmt.Errorf("no encryption key configured")
	}
	plaintext, err := fm.encryptor.Decrypt(data)
	if err != nil {
		return nil, fmt.Errorf("decrypt token data: %w", err)
	}
	var td store.OAuthTokenData
	if err := json.Unmarshal(plaintext, &td); err != nil {
		return nil, fmt.Errorf("unmarshal token data: %w", err)
	}
	return &td, nil
}

func (fm *FlowManager) encryptTokenData(td *store.OAuthTokenData) ([]byte, error) {
	if fm.encryptor == nil {
		return nil, fmt.Errorf("no encryption key configured")
	}
	plaintext, err := json.Marshal(td)
	if err != nil {
		return nil, fmt.Errorf("marshal token data: %w", err)
	}
	encrypted, err := fm.encryptor.Encrypt(plaintext)
	if err != nil {
		return nil, fmt.Errorf("encrypt token data: %w", err)
	}
	return encrypted, nil
}

func (fm *FlowManager) decryptClientSecret(p *store.OAuthProvider) (string, error) {
	if len(p.EncryptedClientSecret) == 0 {
		return "", nil
	}
	if fm.encryptor == nil {
		return "", fmt.Errorf("no encryption key configured")
	}
	plaintext, err := fm.encryptor.Decrypt(p.EncryptedClientSecret)
	if err != nil {
		return "", fmt.Errorf("decrypt client secret: %w", err)
	}
	return string(plaintext), nil
}
