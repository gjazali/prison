package broker

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"prison/internal/vault"
)

const (
	oauthRefreshTimeout   = 15 * time.Second
	oauthRefreshMargin    = 60 * time.Second
	oauthDefaultLifetime  = 5 * time.Minute
	oauthMaxResponseBytes = 1 << 20
)

type oauthRequest struct {
	inmate       string
	profile      string
	variable     string
	tokenURL     string
	clientID     string
	clientSecret string
	refreshToken string
}

type oauthEntry struct {
	mutex       sync.Mutex
	accessToken string
	expiresAt   time.Time
}

type oauthCache struct {
	client  *http.Client
	now     func() time.Time
	mutex   sync.Mutex
	entries map[string]*oauthEntry
}

func newOAuthCache(tlsConfig *tls.Config, now func() time.Time) *oauthCache {
	transport := &http.Transport{
		Proxy:           nil,
		TLSClientConfig: tlsConfig,
	}
	return &oauthCache{
		client:  &http.Client{Transport: transport, Timeout: oauthRefreshTimeout},
		now:     now,
		entries: map[string]*oauthEntry{},
	}
}

func (cache *oauthCache) clear() {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	cache.entries = map[string]*oauthEntry{}
}

func (cache *oauthCache) entryFor(key string) *oauthEntry {
	cache.mutex.Lock()
	defer cache.mutex.Unlock()
	entry, ok := cache.entries[key]
	if !ok {
		entry = &oauthEntry{}
		cache.entries[key] = entry
	}
	return entry
}

func (cache *oauthCache) seed(key, accessToken string, expiresAt time.Time) {
	entry := cache.entryFor(key)
	entry.mutex.Lock()
	entry.accessToken = accessToken
	entry.expiresAt = expiresAt
	entry.mutex.Unlock()
}

func oauthCacheKey(request oauthRequest, refreshToken string) string {
	return vault.InmateCredentialKey(request.inmate, request.profile, request.variable) +
		"|" + request.tokenURL + "|" + request.clientID + "|" + vault.Digest(refreshToken)
}

type oauthTokenResponse struct {
	AccessToken      string `json:"access_token"`
	ExpiresIn        int    `json:"expires_in"`
	RefreshToken     string `json:"refresh_token"`
	Error            string `json:"error"`
	ErrorDescription string `json:"error_description"`
}

func (cache *oauthCache) accessToken(ctx context.Context,
	request oauthRequest) (accessToken, rotatedRefreshToken string, err error) {
	key := oauthCacheKey(request, request.refreshToken)
	entry := cache.entryFor(key)
	entry.mutex.Lock()
	defer entry.mutex.Unlock()

	if entry.accessToken != "" &&
		cache.now().Add(oauthRefreshMargin).Before(entry.expiresAt) {
		return entry.accessToken, "", nil
	}

	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", request.refreshToken)
	if request.clientID != "" {
		form.Set("client_id", request.clientID)
	}
	if request.clientSecret != "" {
		form.Set("client_secret", request.clientSecret)
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost,
		request.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", "", fmt.Errorf("invalid token URL: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	httpRequest.Header.Set("Accept", "application/json")

	response, err := cache.client.Do(httpRequest)
	if err != nil {
		return "", "", fmt.Errorf("token endpoint failed: %w", err)
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, oauthMaxResponseBytes))
	if err != nil {
		return "", "", fmt.Errorf("cannot read token response: %w", err)
	}

	var parsed oauthTokenResponse
	_ = json.Unmarshal(body, &parsed)

	if response.StatusCode != http.StatusOK {
		if parsed.Error != "" {
			if parsed.ErrorDescription != "" {
				return "", "", fmt.Errorf("%s: %s", parsed.Error, parsed.ErrorDescription)
			}
			return "", "", fmt.Errorf("%s", parsed.Error)
		}
		return "", "", fmt.Errorf("token endpoint returned %s", response.Status)
	}
	if parsed.AccessToken == "" {
		return "", "", fmt.Errorf("token response had no access_token")
	}

	lifetime := time.Duration(parsed.ExpiresIn) * time.Second
	if lifetime <= 0 {
		lifetime = oauthDefaultLifetime
	}
	expiresAt := cache.now().Add(lifetime)
	entry.accessToken = parsed.AccessToken
	entry.expiresAt = expiresAt

	if parsed.RefreshToken != "" && parsed.RefreshToken != request.refreshToken {
		rotatedRefreshToken = parsed.RefreshToken
		cache.seed(oauthCacheKey(request, rotatedRefreshToken), parsed.AccessToken, expiresAt)
	}
	return parsed.AccessToken, rotatedRefreshToken, nil
}
