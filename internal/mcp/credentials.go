package mcp

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
)

var (
	masterKey     []byte
	masterKeyOnce sync.Once
	registryMu    sync.Mutex
)

// ForceFallbackForTesting can be set to true in tests to bypass Darwin Keychain.
var ForceFallbackForTesting = false

func getMasterKey() ([]byte, error) {
	var err error
	masterKeyOnce.Do(func() {
		home, errHome := os.UserHomeDir()
		if errHome != nil {
			err = errHome
			return
		}
		keyDir := filepath.Join(home, ".openfabric")
		if errMk := os.MkdirAll(keyDir, 0700); errMk != nil {
			err = errMk
			return
		}
		keyPath := filepath.Join(keyDir, ".credential-key")
		data, errRead := os.ReadFile(keyPath)
		if errRead == nil && len(data) == 32 {
			masterKey = data
			return
		}
		// Generate new key
		key := make([]byte, 32)
		if _, errRand := rand.Read(key); errRand != nil {
			err = errRand
			return
		}
		if errWrite := os.WriteFile(keyPath, key, 0600); errWrite != nil {
			err = errWrite
			return
		}
		masterKey = key
	})
	if err != nil {
		return nil, err
	}
	if len(masterKey) != 32 {
		return nil, fmt.Errorf("invalid master key length")
	}
	return masterKey, nil
}

func encrypt(plaintext []byte) ([]byte, error) {
	key, err := getMasterKey()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	ciphertext := gcm.Seal(nonce, nonce, plaintext, nil)
	return ciphertext, nil
}

func decrypt(ciphertext []byte) ([]byte, error) {
	key, err := getMasterKey()
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return nil, fmt.Errorf("ciphertext too short")
	}
	nonce, actualCiphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	return gcm.Open(nil, nonce, actualCiphertext, nil)
}

func storeEncrypted(service, key, value string) error {
	registryMu.Lock()
	defer registryMu.Unlock()

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	mcpDir := filepath.Join(home, ".openfabric", "mcp")
	if errMk := os.MkdirAll(mcpDir, 0700); errMk != nil {
		return errMk
	}
	filePath := filepath.Join(mcpDir, ".credentials.enc")

	credentials := make(map[string]string)
	if data, err := os.ReadFile(filePath); err == nil {
		plaintext, errDec := decrypt(data)
		if errDec == nil {
			_ = json.Unmarshal(plaintext, &credentials)
		}
	}

	credentials[service+"/"+key] = value
	plaintext, err := json.Marshal(credentials)
	if err != nil {
		return err
	}

	ciphertext, err := encrypt(plaintext)
	if err != nil {
		return err
	}

	return os.WriteFile(filePath, ciphertext, 0600)
}

func getEncrypted(service, key string) (string, error) {
	registryMu.Lock()
	defer registryMu.Unlock()

	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	filePath := filepath.Join(home, ".openfabric", "mcp", ".credentials.enc")

	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}

	plaintext, err := decrypt(data)
	if err != nil {
		return "", err
	}

	credentials := make(map[string]string)
	if err := json.Unmarshal(plaintext, &credentials); err != nil {
		return "", err
	}

	val, ok := credentials[service+"/"+key]
	if !ok {
		return "", fmt.Errorf("credential not found")
	}

	return val, nil
}

func deleteEncrypted(service, key string) error {
	registryMu.Lock()
	defer registryMu.Unlock()

	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	filePath := filepath.Join(home, ".openfabric", "mcp", ".credentials.enc")

	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	plaintext, err := decrypt(data)
	if err != nil {
		return err
	}

	credentials := make(map[string]string)
	if err := json.Unmarshal(plaintext, &credentials); err != nil {
		return err
	}

	delete(credentials, service+"/"+key)

	plaintext, err = json.Marshal(credentials)
	if err != nil {
		return err
	}

	ciphertext, err := encrypt(plaintext)
	if err != nil {
		return err
	}

	return os.WriteFile(filePath, ciphertext, 0600)
}

// StoreCredential saves a credential to the OS keychain, falling back to AES-256-GCM.
func StoreCredential(service, key, value string) error {
	if runtime.GOOS == "darwin" && !ForceFallbackForTesting {
		cmd := exec.Command("security", "add-generic-password",
			"-s", "openfabric-"+service,
			"-a", key,
			"-w", value,
			"-U",
		)
		if err := cmd.Run(); err == nil {
			return nil
		}
	}
	return storeEncrypted(service, key, value)
}

// GetCredential retrieves a credential from the OS keychain, falling back to AES-256-GCM.
func GetCredential(service, key string) (string, error) {
	if runtime.GOOS == "darwin" && !ForceFallbackForTesting {
		out, err := exec.Command("security", "find-generic-password",
			"-s", "openfabric-"+service,
			"-a", key,
			"-w",
		).Output()
		if err == nil {
			return strings.TrimSpace(string(out)), nil
		}
	}
	return getEncrypted(service, key)
}

// DeleteCredential removes a credential from the OS keychain and fallback store.
func DeleteCredential(service, key string) error {
	if runtime.GOOS == "darwin" && !ForceFallbackForTesting {
		cmd := exec.Command("security", "delete-generic-password",
			"-s", "openfabric-"+service,
			"-a", key,
		)
		_ = cmd.Run()
	}
	return deleteEncrypted(service, key)
}
