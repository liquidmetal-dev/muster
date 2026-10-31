package github

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"time"
)

const (
	// jwtBackdate absorbs clock skew between muster and GitHub.
	jwtBackdate = 60 * time.Second
	// jwtLifetime stays under GitHub's 10 minute maximum.
	jwtLifetime = 9 * time.Minute
)

// signAppJWT returns the RS256 JWT that authenticates as the App itself.
func signAppJWT(key *rsa.PrivateKey, appID int64, now time.Time) (string, error) {
	enc := base64.RawURLEncoding
	claims, err := json.Marshal(struct {
		Iat int64  `json:"iat"`
		Exp int64  `json:"exp"`
		Iss string `json:"iss"`
	}{now.Add(-jwtBackdate).Unix(), now.Add(jwtLifetime).Unix(), strconv.FormatInt(appID, 10)})
	if err != nil {
		return "", fmt.Errorf("github: encode jwt claims: %w", err)
	}
	signing := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(nil, key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("github: sign jwt: %w", err)
	}
	return signing + "." + enc.EncodeToString(sig), nil
}
