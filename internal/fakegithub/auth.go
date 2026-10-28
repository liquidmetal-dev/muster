package fakegithub

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/go-github/v93/github"
)

const (
	jwtMaxLifetime = 10 * time.Minute
	jwtIatSkew     = time.Minute
)

// authInfo is the outcome of authenticating one request.
type authInfo struct {
	kind  string // "jwt", "installation", "pat", "none", "invalid"
	login string // PAT owner, for "pat"
}

// authenticate classifies the Authorization header of r.
func (s *Server) authenticate(r *http.Request) authInfo {
	h := r.Header.Get("Authorization")
	if h == "" {
		return authInfo{kind: "none"}
	}
	scheme, token, ok := strings.Cut(h, " ")
	if !ok {
		return authInfo{kind: "invalid"}
	}
	token = strings.TrimSpace(token)
	switch strings.ToLower(scheme) {
	case "bearer", "token":
	default:
		return authInfo{kind: "invalid"}
	}
	if s.installationTokenValid(token) {
		return authInfo{kind: "installation"}
	}
	if login, ok := s.cfg.PATs[token]; ok {
		return authInfo{kind: "pat", login: login}
	}
	if err := s.verifyJWT(token); err == nil {
		return authInfo{kind: "jwt"}
	}
	return authInfo{kind: "invalid"}
}

func (s *Server) installationTokenValid(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	exp, ok := s.tokens[token]
	return ok && s.clock.Now().Before(exp)
}

// verifyJWT checks an RS256 App JWT: signature, iss, exp and iat.
func (s *Server) verifyJWT(token string) error {
	if s.cfg.AppPublicKey == nil {
		return errors.New("no app public key configured")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return errors.New("jwt: want three segments")
	}
	enc := base64.RawURLEncoding
	headerJSON, err := enc.DecodeString(parts[0])
	if err != nil {
		return err
	}
	var header struct{ Alg string }
	if err := json.Unmarshal(headerJSON, &header); err != nil {
		return err
	}
	if header.Alg != "RS256" {
		return errors.New("jwt: alg is not RS256")
	}
	sig, err := enc.DecodeString(parts[2])
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(s.cfg.AppPublicKey, crypto.SHA256, sum[:], sig); err != nil {
		return err
	}
	claimsJSON, err := enc.DecodeString(parts[1])
	if err != nil {
		return err
	}
	var claims struct {
		Iss json.RawMessage `json:"iss"`
		Iat int64           `json:"iat"`
		Exp int64           `json:"exp"`
	}
	if err := json.Unmarshal(claimsJSON, &claims); err != nil {
		return err
	}
	if strings.Trim(string(claims.Iss), `"`) != strconv.FormatInt(s.cfg.AppID, 10) {
		return errors.New("jwt: iss does not match app id")
	}
	now := s.clock.Now()
	exp := time.Unix(claims.Exp, 0)
	if !exp.After(now) {
		return errors.New("jwt: expired")
	}
	if exp.After(now.Add(jwtMaxLifetime)) {
		return errors.New("jwt: exp too far in the future")
	}
	if time.Unix(claims.Iat, 0).After(now.Add(jwtIatSkew)) {
		return errors.New("jwt: iat in the future")
	}
	return nil
}

// issueToken mints an installation token and records its expiry.
func (s *Server) issueToken() (string, time.Time) {
	var b [16]byte
	_, _ = rand.Read(b[:])
	token := "ghs_" + hex.EncodeToString(b[:])
	exp := s.clock.Now().Add(s.cfg.TokenTTL)
	s.mu.Lock()
	s.tokens[token] = exp
	s.mu.Unlock()
	return token, exp
}

func (s *Server) installationMatches(r *http.Request) bool {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return err == nil && id == s.cfg.InstallationID
}

func (s *Server) handleAccessToken(w http.ResponseWriter, r *http.Request, _ authInfo) {
	if !s.installationMatches(r) {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	token, exp := s.issueToken()
	writeJSON(w, http.StatusCreated, &github.InstallationToken{
		Token:     new(token),
		ExpiresAt: &github.Timestamp{Time: exp},
	})
}

func (s *Server) handleInstallation(w http.ResponseWriter, r *http.Request, _ authInfo) {
	if !s.installationMatches(r) {
		writeError(w, http.StatusNotFound, "Not Found")
		return
	}
	writeJSON(w, http.StatusOK, &github.Installation{
		ID:    new(s.cfg.InstallationID),
		AppID: new(s.cfg.AppID),
		Account: &github.User{
			Login: new(s.cfg.InstallationAccount),
			Type:  new("Organization"),
		},
		RepositorySelection: new("all"),
	})
}

func (s *Server) handleUser(w http.ResponseWriter, _ *http.Request, a authInfo) {
	writeJSON(w, http.StatusOK, &github.User{Login: new(a.login), Type: new("User")})
}
