package fakegithub_test

import (
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/liquidmetal-dev/muster/internal/clock"
	"github.com/liquidmetal-dev/muster/internal/fakegithub"
)

func otherKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return k
}

func TestAccessTokenWithValidJWT(t *testing.T) {
	e := start(t)
	now := e.clock.Now()
	jwt := signJWT(t, e.key, "123", now, now.Add(9*time.Minute))
	tok, resp, err := jwtClient(t, e.url, jwt).Apps.CreateInstallationToken(ctx(), installationID, nil)
	if err != nil {
		t.Fatalf("CreateInstallationToken: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status %d, want 201", resp.StatusCode)
	}
	if !strings.HasPrefix(tok.GetToken(), "ghs_") {
		t.Fatalf("token %q lacks ghs_ prefix", tok.GetToken())
	}
	if got := tok.GetExpiresAt().Time; !got.Equal(now.Add(time.Hour)) {
		t.Fatalf("ExpiresAt %v, want %v", got, now.Add(time.Hour))
	}
}

func TestAccessTokenRejectsBadSignature(t *testing.T) {
	e := start(t)
	now := e.clock.Now()
	jwt := signJWT(t, otherKey(t), "123", now, now.Add(9*time.Minute))
	_, _, err := jwtClient(t, e.url, jwt).Apps.CreateInstallationToken(ctx(), installationID, nil)
	wantStatus(t, err, http.StatusUnauthorized)
}

func TestAccessTokenRejectsWrongIssuer(t *testing.T) {
	e := start(t)
	now := e.clock.Now()
	jwt := signJWT(t, e.key, "999", now, now.Add(9*time.Minute))
	_, _, err := jwtClient(t, e.url, jwt).Apps.CreateInstallationToken(ctx(), installationID, nil)
	wantStatus(t, err, http.StatusUnauthorized)
}

func TestAccessTokenRejectsExpTooFar(t *testing.T) {
	e := start(t)
	now := e.clock.Now()
	jwt := signJWT(t, e.key, "123", now, now.Add(11*time.Minute))
	_, _, err := jwtClient(t, e.url, jwt).Apps.CreateInstallationToken(ctx(), installationID, nil)
	wantStatus(t, err, http.StatusUnauthorized)

	jwt = signJWT(t, e.key, "123", now, now.Add(10*time.Minute))
	_, resp, err := jwtClient(t, e.url, jwt).Apps.CreateInstallationToken(ctx(), installationID, nil)
	if err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("exp at +10m: err %v, status %v", err, resp)
	}
}

func TestAccessTokenRejectsFutureIat(t *testing.T) {
	e := start(t)
	now := e.clock.Now()
	jwt := signJWT(t, e.key, "123", now.Add(2*time.Minute), now.Add(9*time.Minute))
	_, _, err := jwtClient(t, e.url, jwt).Apps.CreateInstallationToken(ctx(), installationID, nil)
	wantStatus(t, err, http.StatusUnauthorized)
}

func TestAccessTokenUnknownInstallation(t *testing.T) {
	e := start(t)
	now := e.clock.Now()
	jwt := signJWT(t, e.key, "123", now, now.Add(9*time.Minute))
	_, _, err := jwtClient(t, e.url, jwt).Apps.CreateInstallationToken(ctx(), 457, nil)
	wantStatus(t, err, http.StatusNotFound)
}

func TestInstallationReturnsAccount(t *testing.T) {
	e := start(t)
	now := e.clock.Now()
	jwt := signJWT(t, e.key, "123", now, now.Add(9*time.Minute))
	inst, _, err := jwtClient(t, e.url, jwt).Apps.GetInstallation(ctx(), installationID)
	if err != nil {
		t.Fatalf("GetInstallation: %v", err)
	}
	if inst.GetAccount().GetLogin() != "liquidmetal-dev" || inst.GetID() != installationID || inst.GetAppID() != appID {
		t.Fatalf("installation = %+v", inst)
	}
}

func TestInstallationTokenAuthenticates(t *testing.T) {
	e := start(t)
	_, resp, err := e.client.Actions.ListOrganizationRunners(ctx(), "x", nil)
	if resp == nil || resp.StatusCode == http.StatusUnauthorized {
		t.Fatalf("installation token rejected: %v", err)
	}
}

func TestInstallationTokenExpires(t *testing.T) {
	e := start(t)
	e.clock.Advance(time.Hour + time.Second)
	_, _, err := e.client.Actions.ListOrganizationRunners(ctx(), "x", nil)
	wantStatus(t, err, http.StatusUnauthorized)
}

func TestRealClockShortTTLExpires(t *testing.T) {
	key := fakegithub.AppKey(t)
	fake := fakegithub.New(fakegithub.Config{
		AppID: appID, AppPublicKey: &key.PublicKey, InstallationID: installationID,
		InstallationAccount: "liquidmetal-dev", TokenTTL: 50 * time.Millisecond, Clock: clock.Real(),
	})
	url := fake.Start(t)
	now := time.Now()
	jwt := signJWT(t, key, "123", now, now.Add(9*time.Minute))
	tok, _, err := jwtClient(t, url, jwt).Apps.CreateInstallationToken(ctx(), installationID, nil)
	if err != nil {
		t.Fatalf("CreateInstallationToken: %v", err)
	}
	c := newClient(t, url, tok.GetToken())
	if _, resp, _ := c.Actions.ListOrganizationRunners(ctx(), "x", nil); resp == nil || resp.StatusCode == http.StatusUnauthorized {
		t.Fatal("fresh token rejected")
	}
	time.Sleep(100 * time.Millisecond)
	_, _, err = c.Actions.ListOrganizationRunners(ctx(), "x", nil)
	wantStatus(t, err, http.StatusUnauthorized)
}

func TestTokenSchemeAccepted(t *testing.T) {
	e := start(t)
	resp := rawGet(t, e.url+"/orgs/x/actions/runners", "token "+e.token)
	if resp.StatusCode == http.StatusUnauthorized {
		t.Fatal("token scheme rejected")
	}
}

func TestPATOnUser(t *testing.T) {
	e := start(t)
	u, _, err := newClient(t, e.url, patToken).Users.Get(ctx(), "")
	if err != nil {
		t.Fatalf("Users.Get: %v", err)
	}
	if u.GetLogin() != "octocat" {
		t.Fatalf("login %q", u.GetLogin())
	}
}

func TestInstallationTokenOnUserIs403(t *testing.T) {
	e := start(t)
	_, _, err := e.client.Users.Get(ctx(), "")
	wantStatus(t, err, http.StatusForbidden)
}

func TestNoAuthIs401(t *testing.T) {
	e := start(t)
	resp := rawGet(t, e.url+"/orgs/x/actions/runners", "")
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d, want 401", resp.StatusCode)
	}
	var body struct{ Message string }
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Message != "Bad credentials" {
		t.Fatalf("message %q", body.Message)
	}
}
