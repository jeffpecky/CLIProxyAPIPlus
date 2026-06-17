package xiaomimimo

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"

	"github.com/google/uuid"
)

const (
	PlatformURL    = "https://platform.xiaomimimo.com"
	DefaultKeyName = "mimocode"
	CallbackPort   = 18234
)

func GenerateKeyPair() (publicKeyBase64 string, privateKeyDer []byte, err error) {
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return "", nil, fmt.Errorf("failed to generate key pair: %w", err)
	}
	publicKeyBase64 = base64.RawURLEncoding.EncodeToString(key.PublicKey().Bytes())
	privateKeyDer = key.Bytes()
	return publicKeyBase64, privateKeyDer, nil
}

func Decrypt(privateKeyDer []byte, encryptedBase64 string) (sk string, uid string, retURL string, err error) {
	encrypted, err := base64.RawURLEncoding.DecodeString(encryptedBase64)
	if err != nil {
		return "", "", "", fmt.Errorf("base64 decode: %w", err)
	}
	if len(encrypted) < 32+12+16 {
		return "", "", "", fmt.Errorf("encrypted data too short")
	}
	ephemeralPub := encrypted[:32]
	nonce := encrypted[32:44]
	ciphertextAndTag := encrypted[44:]

	privateKey, err := ecdh.X25519().NewPrivateKey(privateKeyDer)
	if err != nil {
		return "", "", "", fmt.Errorf("private key: %w", err)
	}

	spkiHeader, _ := hex.DecodeString("302a300506032b656e032100")
	ephemeralPubBytes := append(spkiHeader, ephemeralPub...)
	ephemeralPubKey, err := ecdh.X25519().NewPublicKey(ephemeralPubBytes)
	if err != nil {
		return "", "", "", fmt.Errorf("ephemeral public key: %w", err)
	}

	sharedSecret, err := privateKey.ECDH(ephemeralPubKey)
	if err != nil {
		return "", "", "", fmt.Errorf("ECDH: %w", err)
	}

	h := sha256.Sum256(sharedSecret)
	derivedKey := h[:]

	block, err := aes.NewCipher(derivedKey)
	if err != nil {
		return "", "", "", fmt.Errorf("aes cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", "", "", fmt.Errorf("gcm: %w", err)
	}
	plaintext, err := gcm.Open(nil, nonce, ciphertextAndTag, nil)
	if err != nil {
		return "", "", "", fmt.Errorf("decrypt: %w", err)
	}

	var result struct {
		SK  string `json:"sk"`
		UID string `json:"uid"`
		URL string `json:"url"`
	}
	if err := json.Unmarshal(plaintext, &result); err != nil {
		return "", "", "", fmt.Errorf("parse json: %w", err)
	}
	return result.SK, result.UID, result.URL, nil
}

func BuildAuthorizeURL(publicKey, redirectUri, keyName string) string {
	params := url.Values{
		"pk":           {publicKey},
		"redirect_uri": {redirectUri},
		"kn":           {keyName},
		"key_name":     {keyName},
	}
	return fmt.Sprintf("%s/authorize?%s", PlatformURL, params.Encode())
}

func StartOAuthServer(port int, handler func(code string)) (*http.Server, error) {
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		code := r.URL.Query().Get("u")
		if code == "" {
			http.Error(w, "missing code", http.StatusBadRequest)
			return
		}
		handler(code)
		fmt.Fprintf(w, "Authorization successful! You can close this window.")
	})
	server := &http.Server{Addr: fmt.Sprintf(":%d", port), Handler: mux}
	go func() {
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fmt.Printf("OAuth server error: %v\n", err)
		}
	}()
	return server, nil
}

func GenerateKeyName() string {
	return fmt.Sprintf("mimo-code-cli-key-%s", uuid.New().String()[:8])
}
