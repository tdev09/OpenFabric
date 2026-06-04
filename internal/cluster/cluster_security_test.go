package cluster

import (
	"sync"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Fix 5.1 - Cluster Secret Encryption at Transport
// ---------------------------------------------------------------------------

func TestEncryptDecryptClusterSecret_Roundtrip(t *testing.T) {
	t.Parallel()

	secret := []byte("super-secret-cluster-key-32bytes!")
	token := "abc123"

	ciphertext, err := EncryptClusterSecret(secret, token)
	if err != nil {
		t.Fatalf("EncryptClusterSecret: %v", err)
	}

	// Must not be plaintext
	if string(ciphertext) == string(secret) {
		t.Error("encrypted output should differ from plaintext secret")
	}

	plaintext, err := DecryptClusterSecret(ciphertext, token)
	if err != nil {
		t.Fatalf("DecryptClusterSecret: %v", err)
	}
	if string(plaintext) != string(secret) {
		t.Errorf("decrypted secret mismatch: got %q, want %q", plaintext, secret)
	}
}

func TestEncryptClusterSecret_DifferentTokensFail(t *testing.T) {
	t.Parallel()

	secret := []byte("cluster-secret")
	ciphertext, err := EncryptClusterSecret(secret, "correct-token")
	if err != nil {
		t.Fatalf("EncryptClusterSecret: %v", err)
	}

	// Decrypting with a wrong token must fail (GCM auth tag mismatch)
	_, err = DecryptClusterSecret(ciphertext, "wrong-token")
	if err == nil {
		t.Error("decryption with wrong token should fail, but got nil error")
	}
}

func TestEncryptClusterSecret_NoncesAreDifferent(t *testing.T) {
	t.Parallel()

	secret := []byte("cluster-secret")
	token := "abc123"

	c1, _ := EncryptClusterSecret(secret, token)
	c2, _ := EncryptClusterSecret(secret, token)

	// Each call uses a fresh random nonce so ciphertexts should differ
	if string(c1) == string(c2) {
		t.Error("two encryptions of the same secret should produce different ciphertexts (randomised nonce)")
	}
}

// ---------------------------------------------------------------------------
// Fix 5.7 - Token Race: UseJoinToken is Atomic
// ---------------------------------------------------------------------------

func TestUseJoinToken_AtomicSingleUse(t *testing.T) {
	t.Parallel()

	mgr := NewManager(nil)
	tok, err := mgr.GenerateJoinToken()
	if err != nil {
		t.Fatalf("GenerateJoinToken: %v", err)
	}

	const goroutines = 50
	results := make([]bool, goroutines)
	var wg sync.WaitGroup

	for i := 0; i < goroutines; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = mgr.UseJoinToken(tok.Token)
		}()
	}
	wg.Wait()

	// Exactly one goroutine must have succeeded
	successCount := 0
	for _, ok := range results {
		if ok {
			successCount++
		}
	}
	if successCount != 1 {
		t.Errorf("expected exactly 1 successful UseJoinToken across %d goroutines, got %d", goroutines, successCount)
	}

	// Token must be gone from the map after use
	if mgr.ValidateJoinToken(tok.Token) {
		t.Error("token should be invalid after UseJoinToken consumed it")
	}
}

func TestUseJoinToken_ExpiredTokenDeletedAndDenied(t *testing.T) {
	t.Parallel()

	mgr := NewManager(nil)
	tok, err := mgr.GenerateJoinToken()
	if err != nil {
		t.Fatalf("GenerateJoinToken: %v", err)
	}

	// Manually expire the token
	mgr.mu.Lock()
	mgr.joinTokens[tok.Token].ExpiresAt = time.Now().Add(-1 * time.Second)
	mgr.mu.Unlock()

	ok := mgr.UseJoinToken(tok.Token)
	if ok {
		t.Error("UseJoinToken should return false for an expired token")
	}

	// Token must also be removed from the map (GC)
	mgr.mu.RLock()
	_, exists := mgr.joinTokens[tok.Token]
	mgr.mu.RUnlock()
	if exists {
		t.Error("expired token should be deleted from the map by UseJoinToken (Fix 5.7 GC)")
	}
}
