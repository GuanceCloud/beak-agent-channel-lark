package lark

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

func VerifyWebhookSignature(timestamp, nonce, encryptKey string, body []byte, signature string) bool {
	signature = strings.TrimSpace(strings.ToLower(signature))
	if signature == "" {
		return false
	}
	sum := sha256.Sum256([]byte(timestamp + nonce + encryptKey + string(body)))
	expected := hex.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(expected), []byte(signature)) == 1
}

func DecodeWebhookBody(body []byte, encryptKey string) ([]byte, error) {
	var encrypted struct {
		Encrypt string `json:"encrypt"`
	}
	if err := json.Unmarshal(body, &encrypted); err != nil || strings.TrimSpace(encrypted.Encrypt) == "" {
		return body, nil
	}
	if strings.TrimSpace(encryptKey) == "" {
		return nil, fmt.Errorf("lark encrypted webhook payload requires encrypt_key")
	}
	plain, err := DecryptEvent(encrypted.Encrypt, encryptKey)
	if err != nil {
		return nil, err
	}
	return []byte(plain), nil
}

func DecryptEvent(encrypted, encryptKey string) (string, error) {
	buf, err := base64.StdEncoding.DecodeString(encrypted)
	if err != nil {
		return "", fmt.Errorf("decode lark encrypted event: %w", err)
	}
	if len(buf) < aes.BlockSize {
		return "", fmt.Errorf("decode lark encrypted event: ciphertext too short")
	}
	key := sha256.Sum256([]byte(encryptKey))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return "", err
	}
	iv := buf[:aes.BlockSize]
	ciphertext := buf[aes.BlockSize:]
	if len(ciphertext)%aes.BlockSize != 0 {
		return "", fmt.Errorf("decode lark encrypted event: ciphertext is not a multiple of block size")
	}
	mode := cipher.NewCBCDecrypter(block, iv)
	plain := make([]byte, len(ciphertext))
	mode.CryptBlocks(plain, ciphertext)
	plain, err = pkcs7Unpad(plain, aes.BlockSize)
	if err != nil {
		return "", err
	}
	return string(plain), nil
}

func pkcs7Unpad(data []byte, blockSize int) ([]byte, error) {
	if len(data) == 0 || len(data)%blockSize != 0 {
		return nil, fmt.Errorf("invalid pkcs7 data")
	}
	padding := int(data[len(data)-1])
	if padding == 0 || padding > blockSize || padding > len(data) {
		return nil, fmt.Errorf("invalid pkcs7 padding")
	}
	if !bytes.Equal(data[len(data)-padding:], bytes.Repeat([]byte{byte(padding)}, padding)) {
		return nil, fmt.Errorf("invalid pkcs7 padding")
	}
	return data[:len(data)-padding], nil
}
