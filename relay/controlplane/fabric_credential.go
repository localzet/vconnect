package controlplane

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const fabricTokenPrefix = "ft1"

func IssueFabricToken(key []byte, userID string, ttl time.Duration) (string, error) {
	if len(key) < 32 {
		return "", errors.New("controlplane: Fabric credential key must contain at least 32 bytes")
	}
	userID = normalizeUserID(userID)
	if userID == "" {
		return "", errors.New("controlplane: user id is required")
	}
	if ttl <= 0 || ttl > 24*time.Hour {
		return "", errors.New("controlplane: Fabric token TTL must be between 1ns and 24h")
	}
	expires := time.Now().Add(ttl).Unix()
	nonceRaw := make([]byte, 16)
	if _, err := rand.Read(nonceRaw); err != nil {
		return "", err
	}
	nonce := base64.RawURLEncoding.EncodeToString(nonceRaw)
	signature := fabricTokenSignature(key, userID, expires, nonce)
	return fmt.Sprintf("%s.%d.%s.%s", fabricTokenPrefix, expires, nonce, signature), nil
}

func VerifyFabricCredential(key []byte, userID, credential string, now time.Time) bool {
	userID = normalizeUserID(userID)
	credential = strings.TrimSpace(credential)
	if userID == "" || credential == "" || len(key) < 32 {
		return false
	}

	if strings.HasPrefix(credential, fabricTokenPrefix+".") {
		return verifyFabricToken(key, userID, credential, now)
	}

	expected := DeriveFabricCredential(key, userID)
	if len(expected) != len(credential) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(credential)) == 1
}

func verifyFabricToken(key []byte, userID, token string, now time.Time) bool {
	parts := strings.Split(token, ".")
	if len(parts) != 4 || parts[0] != fabricTokenPrefix {
		return false
	}
	expires, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil {
		return false
	}
	if expires <= now.Unix() || expires > now.Add(24*time.Hour).Unix()+30 {
		return false
	}
	nonce := parts[2]
	if len(nonce) < 16 || len(nonce) > 64 {
		return false
	}
	expected := fabricTokenSignature(key, userID, expires, nonce)
	if len(expected) != len(parts[3]) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(parts[3])) == 1
}

func fabricTokenSignature(key []byte, userID string, expires int64, nonce string) string {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte("vconnect-fabric/v1/token|"))
	_, _ = mac.Write([]byte(normalizeUserID(userID)))
	_, _ = mac.Write([]byte("|"))
	_, _ = mac.Write([]byte(strconv.FormatInt(expires, 10)))
	_, _ = mac.Write([]byte("|"))
	_, _ = mac.Write([]byte(nonce))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
