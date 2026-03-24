// Package image provides the OCI registry client for pulling container images.
package image

import (
	"compress/gzip"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// RegistryClient implements the OCI Distribution Spec for pulling images
// from Docker Hub, GCR, ECR, GHCR, and any OCI-compliant registry.
type RegistryClient struct {
	httpClient *http.Client
	tokens     map[string]tokenEntry
	credStore  *CredentialStore
}

type tokenEntry struct {
	token   string
	expires time.Time
}

// OCIManifest represents an OCI image manifest
type OCIManifest struct {
	SchemaVersion int              `json:"schemaVersion"`
	MediaType     string           `json:"mediaType"`
	Config        OCIDescriptor    `json:"config"`
	Layers        []OCIDescriptor  `json:"layers"`
}

// OCIManifestList represents a manifest list (fat manifest / index)
type OCIManifestList struct {
	SchemaVersion int                `json:"schemaVersion"`
	MediaType     string             `json:"mediaType"`
	Manifests     []OCIManifestEntry `json:"manifests"`
}

// OCIManifestEntry is one entry in a manifest list
type OCIManifestEntry struct {
	MediaType string         `json:"mediaType"`
	Digest    string         `json:"digest"`
	Size      int64          `json:"size"`
	Platform  *OCIPlatform   `json:"platform,omitempty"`
}

// OCIPlatform describes the platform of a manifest entry
type OCIPlatform struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	Variant      string `json:"variant,omitempty"`
}

// OCIDescriptor is a content-addressable descriptor
type OCIDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

// tokenResponse is the response from a token endpoint
type tokenResponse struct {
	Token       string `json:"token"`
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

// NewRegistryClient creates a new OCI registry client
func NewRegistryClient() *RegistryClient {
	return &RegistryClient{
		httpClient: &http.Client{
			Timeout: 5 * time.Minute,
		},
		tokens: make(map[string]tokenEntry),
	}
}

// NewRegistryClientWithCreds creates a RegistryClient that uses stored credentials
// for authenticating with private registries.
func NewRegistryClientWithCreds(credStore *CredentialStore) *RegistryClient {
	return &RegistryClient{
		httpClient: &http.Client{
			Timeout: 5 * time.Minute,
		},
		tokens:    make(map[string]tokenEntry),
		credStore: credStore,
	}
}

// FetchManifest retrieves and resolves the image manifest for the given reference.
// It handles manifest lists by selecting the best platform match (preferring linux/amd64).
func (c *RegistryClient) FetchManifest(registry, repo, tag string) (*OCIManifest, error) {
	url := fmt.Sprintf("https://%s/v2/%s/manifests/%s", registry, repo, tag)

	// Accept both manifest and manifest list types
	acceptTypes := strings.Join([]string{
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
	}, ", ")

	body, mediaType, err := c.registryGet(registry, repo, url, acceptTypes)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch manifest: %w", err)
	}
	defer body.Close()

	data, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("failed to read manifest: %w", err)
	}

	// If this is a manifest list / index, resolve to a single manifest
	if isManifestList(mediaType) {
		var list OCIManifestList
		if err := json.Unmarshal(data, &list); err != nil {
			return nil, fmt.Errorf("failed to parse manifest list: %w", err)
		}

		digest, err := selectPlatform(list.Manifests)
		if err != nil {
			return nil, err
		}

		// Re-fetch the resolved manifest by digest
		return c.FetchManifest(registry, repo, digest)
	}

	var manifest OCIManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("failed to parse manifest: %w", err)
	}

	return &manifest, nil
}

// FetchManifestRaw retrieves the image manifest and returns both the parsed
// manifest and the raw JSON bytes (for digest computation). It handles manifest
// lists by selecting the best platform match.
func (c *RegistryClient) FetchManifestRaw(registry, repo, tag string) (*OCIManifest, []byte, error) {
	url := fmt.Sprintf("https://%s/v2/%s/manifests/%s", registry, repo, tag)

	acceptTypes := strings.Join([]string{
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
	}, ", ")

	body, mediaType, err := c.registryGet(registry, repo, url, acceptTypes)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to fetch manifest: %w", err)
	}
	defer body.Close()

	data, err := io.ReadAll(body)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read manifest: %w", err)
	}

	// If this is a manifest list / index, resolve to a single manifest
	if isManifestList(mediaType) {
		var list OCIManifestList
		if err := json.Unmarshal(data, &list); err != nil {
			return nil, nil, fmt.Errorf("failed to parse manifest list: %w", err)
		}

		digest, err := selectPlatform(list.Manifests)
		if err != nil {
			return nil, nil, err
		}

		// Re-fetch the resolved manifest by digest
		return c.FetchManifestRaw(registry, repo, digest)
	}

	var manifest OCIManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, nil, fmt.Errorf("failed to parse manifest: %w", err)
	}

	return &manifest, data, nil
}

// HeadManifest performs an HTTP HEAD request to get the manifest digest without
// downloading the full manifest body. The digest is read from the
// Docker-Content-Digest response header. If HEAD is not supported by the
// registry, it falls back to a full GET and computes the digest locally.
func (c *RegistryClient) HeadManifest(registry, repo, tag string) (string, error) {
	url := fmt.Sprintf("https://%s/v2/%s/manifests/%s", registry, repo, tag)

	acceptTypes := strings.Join([]string{
		"application/vnd.oci.image.manifest.v1+json",
		"application/vnd.docker.distribution.manifest.v2+json",
		"application/vnd.oci.image.index.v1+json",
		"application/vnd.docker.distribution.manifest.list.v2+json",
	}, ", ")

	// Try HEAD first
	resp, err := c.registryHead(registry, repo, url, acceptTypes)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			digest := resp.Header.Get("Docker-Content-Digest")
			if digest != "" {
				return digest, nil
			}
		}
	}

	// Fall back to GET and compute digest
	body, _, err := c.registryGet(registry, repo, url, acceptTypes)
	if err != nil {
		return "", fmt.Errorf("failed to fetch manifest for digest: %w", err)
	}
	defer body.Close()

	data, err := io.ReadAll(body)
	if err != nil {
		return "", fmt.Errorf("failed to read manifest: %w", err)
	}

	return computeSHA256Bytes(data), nil
}

// registryHead performs an authenticated HEAD request to a registry endpoint.
// It handles the WWW-Authenticate challenge flow for bearer token auth.
func (c *RegistryClient) registryHead(registry, repo, reqURL, accept string) (*http.Response, error) {
	req, err := http.NewRequest("HEAD", reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", accept)

	// Use cached token if available
	tokenKey := registry + "/" + repo
	if te, ok := c.tokens[tokenKey]; ok && time.Now().Before(te.expires) {
		req.Header.Set("Authorization", "Bearer "+te.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HEAD request failed: %w", err)
	}

	// Handle auth challenge
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()

		challenge := resp.Header.Get("Www-Authenticate")
		if challenge == "" {
			return nil, fmt.Errorf("registry returned 401 with no WWW-Authenticate header")
		}

		token, expiresIn, err := c.fetchToken(challenge, registry, repo)
		if err != nil {
			return nil, fmt.Errorf("failed to authenticate: %w", err)
		}

		c.tokens[tokenKey] = tokenEntry{
			token:   token,
			expires: time.Now().Add(time.Duration(expiresIn) * time.Second),
		}

		req2, _ := http.NewRequest("HEAD", reqURL, nil)
		req2.Header.Set("Accept", accept)
		req2.Header.Set("Authorization", "Bearer "+token)

		resp, err = c.httpClient.Do(req2)
		if err != nil {
			return nil, fmt.Errorf("authenticated HEAD request failed: %w", err)
		}
	}

	return resp, nil
}

// computeSHA256Bytes computes sha256 digest of raw bytes in OCI format "sha256:<hex>".
func computeSHA256Bytes(data []byte) string {
	h := sha256.New()
	h.Write(data)
	return fmt.Sprintf("sha256:%x", h.Sum(nil))
}

// FetchLayers converts manifest layers into LayerInfo structs with download URLs
func (c *RegistryClient) FetchLayers(registry, repo string, manifest *OCIManifest) []LayerInfo {
	layers := make([]LayerInfo, 0, len(manifest.Layers))
	for _, desc := range manifest.Layers {
		layers = append(layers, LayerInfo{
			Digest:    desc.Digest,
			Size:      desc.Size,
			URL:       fmt.Sprintf("https://%s/v2/%s/blobs/%s", registry, repo, desc.Digest),
			MediaType: desc.MediaType,
		})
	}
	return layers
}

// DownloadLayer streams a layer blob from the registry, decompressing gzip layers.
// The caller is responsible for closing the returned reader.
func (c *RegistryClient) DownloadLayer(registry, repo string, layer LayerInfo) (io.ReadCloser, error) {
	body, _, err := c.registryGet(registry, repo, layer.URL, "application/octet-stream")
	if err != nil {
		return nil, fmt.Errorf("failed to download layer %s: %w", layer.Digest, err)
	}

	// Decompress gzip layers on the fly
	if isGzipLayer(layer.MediaType) {
		gz, err := gzip.NewReader(body)
		if err != nil {
			body.Close()
			return nil, fmt.Errorf("failed to create gzip reader for %s: %w", layer.Digest, err)
		}
		return &gzipReadCloser{gz: gz, underlying: body}, nil
	}

	return body, nil
}

// DownloadLayerRaw streams the raw (compressed) layer blob from the registry
// without decompressing. This is suitable for caching the original blob so
// that its digest can be verified. The caller is responsible for closing the
// returned reader.
func (c *RegistryClient) DownloadLayerRaw(registry, repo string, layer LayerInfo) (io.ReadCloser, error) {
	body, _, err := c.registryGet(registry, repo, layer.URL, "application/octet-stream")
	if err != nil {
		return nil, fmt.Errorf("failed to download layer %s: %w", layer.Digest, err)
	}
	return body, nil
}

// registryRetryConfig controls retry behavior for registry requests.
const (
	registryMaxRetries    = 3
	registryBaseDelay     = 1 * time.Second
	registryMaxDelay      = 30 * time.Second
)

// isRetryableStatus returns true for HTTP status codes that should be retried.
func isRetryableStatus(code int) bool {
	return code == http.StatusTooManyRequests ||
		code == http.StatusServiceUnavailable ||
		code == http.StatusGatewayTimeout ||
		code == http.StatusBadGateway
}

// parseRetryAfter extracts the delay from a Retry-After header.
// It handles both integer seconds ("120") and HTTP-date formats.
// Returns 0 if the header is missing or unparseable.
func parseRetryAfter(resp *http.Response) time.Duration {
	val := resp.Header.Get("Retry-After")
	if val == "" {
		return 0
	}

	// Try integer seconds first
	if secs, err := strconv.Atoi(val); err == nil && secs > 0 {
		d := time.Duration(secs) * time.Second
		if d > registryMaxDelay {
			d = registryMaxDelay
		}
		return d
	}

	// Try HTTP-date format (RFC 1123)
	if t, err := time.Parse(time.RFC1123, val); err == nil {
		d := time.Until(t)
		if d <= 0 {
			return registryBaseDelay
		}
		if d > registryMaxDelay {
			d = registryMaxDelay
		}
		return d
	}

	return 0
}

// retryDelay calculates the delay for a given retry attempt, honoring
// the Retry-After header if present, otherwise using exponential backoff.
func retryDelay(attempt int, resp *http.Response) time.Duration {
	if resp != nil {
		if d := parseRetryAfter(resp); d > 0 {
			return d
		}
	}
	// Exponential backoff: 1s, 2s, 4s, ...
	d := registryBaseDelay << uint(attempt)
	if d > registryMaxDelay {
		d = registryMaxDelay
	}
	return d
}

// registryGet performs an authenticated GET to a registry endpoint.
// It handles the WWW-Authenticate challenge flow for bearer token auth
// and retries on HTTP 429 (rate limit) and 5xx server errors with
// exponential backoff.
func (c *RegistryClient) registryGet(registry, repo, url, accept string) (io.ReadCloser, string, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("Accept", accept)

	// Use cached token if available
	tokenKey := registry + "/" + repo
	if te, ok := c.tokens[tokenKey]; ok && time.Now().Before(te.expires) {
		req.Header.Set("Authorization", "Bearer "+te.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("request failed: %w", err)
	}

	// Handle auth challenge
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()

		challenge := resp.Header.Get("Www-Authenticate")
		if challenge == "" {
			return nil, "", fmt.Errorf("registry returned 401 with no WWW-Authenticate header")
		}

		token, expiresIn, err := c.fetchToken(challenge, registry, repo)
		if err != nil {
			return nil, "", fmt.Errorf("failed to authenticate: %w", err)
		}

		c.tokens[tokenKey] = tokenEntry{
			token:   token,
			expires: time.Now().Add(time.Duration(expiresIn) * time.Second),
		}

		// Retry with token
		req2, _ := http.NewRequest("GET", url, nil)
		req2.Header.Set("Accept", accept)
		req2.Header.Set("Authorization", "Bearer "+token)

		resp, err = c.httpClient.Do(req2)
		if err != nil {
			return nil, "", fmt.Errorf("authenticated request failed: %w", err)
		}
	}

	// Retry on rate-limit (429) and server errors (502, 503, 504)
	for attempt := 0; attempt < registryMaxRetries && isRetryableStatus(resp.StatusCode); attempt++ {
		delay := retryDelay(attempt, resp)
		resp.Body.Close()
		time.Sleep(delay)

		retryReq, _ := http.NewRequest("GET", url, nil)
		retryReq.Header.Set("Accept", accept)
		if te, ok := c.tokens[tokenKey]; ok && time.Now().Before(te.expires) {
			retryReq.Header.Set("Authorization", "Bearer "+te.token)
		}

		resp, err = c.httpClient.Do(retryReq)
		if err != nil {
			return nil, "", fmt.Errorf("retry request failed: %w", err)
		}
	}

	// Follow redirects for blob downloads (some registries redirect to CDN)
	if resp.StatusCode == http.StatusTemporaryRedirect || resp.StatusCode == http.StatusFound {
		loc := resp.Header.Get("Location")
		resp.Body.Close()
		if loc == "" {
			return nil, "", fmt.Errorf("redirect with no Location header")
		}
		redirectReq, _ := http.NewRequest("GET", loc, nil)
		resp, err = c.httpClient.Do(redirectReq)
		if err != nil {
			return nil, "", fmt.Errorf("redirect request failed: %w", err)
		}
	}

	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, "", fmt.Errorf("registry returned status %d for %s", resp.StatusCode, url)
	}

	ct := resp.Header.Get("Content-Type")
	return resp.Body, ct, nil
}

// fetchToken performs the OAuth2 token exchange with the registry's token service.
// It parses the WWW-Authenticate header to extract realm, service, and scope.
func (c *RegistryClient) fetchToken(challenge, registry, repo string) (string, int, error) {
	params := parseWWWAuthenticate(challenge)

	realm := params["realm"]
	if realm == "" {
		return "", 0, fmt.Errorf("no realm in WWW-Authenticate header")
	}

	// Build token request URL
	tokenURL := realm + "?"
	if svc, ok := params["service"]; ok {
		tokenURL += "service=" + url.QueryEscape(svc) + "&"
	}
	if scope, ok := params["scope"]; ok {
		tokenURL += "scope=" + url.QueryEscape(scope)
	} else {
		tokenURL += "scope=repository:" + url.QueryEscape(repo) + ":pull"
	}

	req, err := http.NewRequest("GET", tokenURL, nil)
	if err != nil {
		return "", 0, fmt.Errorf("token request failed: %w", err)
	}
	c.applyBasicAuth(req, registry)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("token endpoint returned status %d", resp.StatusCode)
	}

	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", 0, fmt.Errorf("failed to decode token response: %w", err)
	}

	token := tr.Token
	if token == "" {
		token = tr.AccessToken
	}
	if token == "" {
		return "", 0, fmt.Errorf("no token in response")
	}

	expiresIn := tr.ExpiresIn
	if expiresIn == 0 {
		expiresIn = 300 // default 5 minutes
	}

	return token, expiresIn, nil
}

// parseWWWAuthenticate parses a Bearer WWW-Authenticate header into key=value pairs.
// Example: `Bearer realm="https://auth.docker.io/token",service="registry.docker.io",scope="repository:library/ubuntu:pull"`
func parseWWWAuthenticate(header string) map[string]string {
	params := make(map[string]string)

	// Strip "Bearer " prefix
	header = strings.TrimPrefix(header, "Bearer ")
	header = strings.TrimPrefix(header, "bearer ")

	// Parse key="value" pairs
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		eq := strings.IndexByte(part, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(part[:eq])
		val := strings.TrimSpace(part[eq+1:])
		val = strings.Trim(val, "\"")
		params[key] = val
	}

	return params
}

// selectPlatform picks the best manifest from a manifest list.
// Preference order: linux/amd64, linux/arm64, first linux entry, first entry.
func selectPlatform(entries []OCIManifestEntry) (string, error) {
	if len(entries) == 0 {
		return "", fmt.Errorf("empty manifest list")
	}

	// Prefer the host architecture so the guest kernel can run the binaries
	hostArch := runtime.GOARCH // "arm64" on Apple Silicon, "amd64" on Intel
	var preferences []struct{ arch, variant string }
	if hostArch == "arm64" {
		preferences = []struct{ arch, variant string }{
			{"arm64", ""},
			{"amd64", ""},
		}
	} else {
		preferences = []struct{ arch, variant string }{
			{"amd64", ""},
			{"arm64", ""},
		}
	}

	for _, pref := range preferences {
		for _, e := range entries {
			if e.Platform != nil && e.Platform.OS == "linux" && e.Platform.Architecture == pref.arch {
				if pref.variant == "" || e.Platform.Variant == pref.variant {
					return e.Digest, nil
				}
			}
		}
	}

	// Fallback: any linux entry
	for _, e := range entries {
		if e.Platform != nil && e.Platform.OS == "linux" {
			return e.Digest, nil
		}
	}

	// Last resort: first entry
	return entries[0].Digest, nil
}

// registryAuth performs an authenticated request to a registry endpoint.
// It handles the WWW-Authenticate challenge flow for bearer token auth with push scope.
func (c *RegistryClient) registryRequest(method, registry, repo, url, contentType string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}

	// Use cached token if available
	tokenKey := registry + "/" + repo + ":push"
	if te, ok := c.tokens[tokenKey]; ok && time.Now().Before(te.expires) {
		req.Header.Set("Authorization", "Bearer "+te.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}

	// Handle auth challenge
	if resp.StatusCode == http.StatusUnauthorized {
		resp.Body.Close()

		challenge := resp.Header.Get("Www-Authenticate")
		if challenge == "" {
			return nil, fmt.Errorf("registry returned 401 with no WWW-Authenticate header")
		}

		token, expiresIn, err := c.fetchTokenWithScope(challenge, registry, repo, "push,pull")
		if err != nil {
			return nil, fmt.Errorf("failed to authenticate: %w", err)
		}

		c.tokens[tokenKey] = tokenEntry{
			token:   token,
			expires: time.Now().Add(time.Duration(expiresIn) * time.Second),
		}

		// Retry with token — need to re-create the request since body may have been consumed
		req2, err := http.NewRequest(method, url, body)
		if err != nil {
			return nil, err
		}
		if contentType != "" {
			req2.Header.Set("Content-Type", contentType)
		}
		req2.Header.Set("Authorization", "Bearer "+token)

		resp, err = c.httpClient.Do(req2)
		if err != nil {
			return nil, fmt.Errorf("authenticated request failed: %w", err)
		}
	}

	// Retry on rate-limit (429) and server errors (502, 503, 504).
	// Only retry idempotent methods or methods where the caller can handle re-send.
	if method == "GET" || method == "HEAD" {
		for attempt := 0; attempt < registryMaxRetries && isRetryableStatus(resp.StatusCode); attempt++ {
			delay := retryDelay(attempt, resp)
			resp.Body.Close()
			time.Sleep(delay)

			retryReq, err := http.NewRequest(method, url, nil)
			if err != nil {
				return nil, err
			}
			if contentType != "" {
				retryReq.Header.Set("Content-Type", contentType)
			}
			tokenKey := registry + "/" + repo + ":push"
			if te, ok := c.tokens[tokenKey]; ok && time.Now().Before(te.expires) {
				retryReq.Header.Set("Authorization", "Bearer "+te.token)
			}

			resp, err = c.httpClient.Do(retryReq)
			if err != nil {
				return nil, fmt.Errorf("retry request failed: %w", err)
			}
		}
	}

	return resp, nil
}

// fetchTokenWithScope performs token exchange with a specific scope (e.g., "push,pull").
func (c *RegistryClient) fetchTokenWithScope(challenge, registry, repo, scope string) (string, int, error) {
	params := parseWWWAuthenticate(challenge)

	realm := params["realm"]
	if realm == "" {
		return "", 0, fmt.Errorf("no realm in WWW-Authenticate header")
	}

	tokenURL := realm + "?"
	if svc, ok := params["service"]; ok {
		tokenURL += "service=" + url.QueryEscape(svc) + "&"
	}
	tokenURL += "scope=repository:" + url.QueryEscape(repo) + ":" + url.QueryEscape(scope)

	req, err := http.NewRequest("GET", tokenURL, nil)
	if err != nil {
		return "", 0, fmt.Errorf("token request failed: %w", err)
	}
	c.applyBasicAuth(req, registry)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("token endpoint returned status %d", resp.StatusCode)
	}

	var tr tokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		return "", 0, fmt.Errorf("failed to decode token response: %w", err)
	}

	token := tr.Token
	if token == "" {
		token = tr.AccessToken
	}
	if token == "" {
		return "", 0, fmt.Errorf("no token in response")
	}

	expiresIn := tr.ExpiresIn
	if expiresIn == 0 {
		expiresIn = 300
	}

	return token, expiresIn, nil
}

// CheckBlobExists checks whether a blob with the given digest already exists in the registry.
func (c *RegistryClient) CheckBlobExists(registry, repo, digest string) (bool, error) {
	url := fmt.Sprintf("https://%s/v2/%s/blobs/%s", registry, repo, digest)
	resp, err := c.registryRequest("HEAD", registry, repo, url, "", nil)
	if err != nil {
		return false, err
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK, nil
}

// UploadBlob uploads a blob to the registry using the monolithic upload method.
// Returns the digest string of the uploaded blob.
func (c *RegistryClient) UploadBlob(registry, repo, digest string, size int64, content io.ReadSeeker) error {
	// Step 1: Initiate upload
	initiateURL := fmt.Sprintf("https://%s/v2/%s/blobs/uploads/", registry, repo)
	resp, err := c.registryRequest("POST", registry, repo, initiateURL, "", nil)
	if err != nil {
		return fmt.Errorf("failed to initiate blob upload: %w", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("registry returned status %d for upload initiation", resp.StatusCode)
	}

	// Get the upload URL from the Location header
	uploadURL := resp.Header.Get("Location")
	if uploadURL == "" {
		return fmt.Errorf("registry did not return upload Location header")
	}

	// Make the upload URL absolute if needed
	if !strings.HasPrefix(uploadURL, "http") {
		uploadURL = fmt.Sprintf("https://%s%s", registry, uploadURL)
	}

	// Append the digest query parameter
	sep := "?"
	if strings.Contains(uploadURL, "?") {
		sep = "&"
	}
	uploadURL += sep + "digest=" + digest

	// Step 2: Upload the blob content
	content.Seek(0, io.SeekStart)
	resp, err = c.registryRequest("PUT", registry, repo, uploadURL, "application/octet-stream", content)
	if err != nil {
		return fmt.Errorf("failed to upload blob: %w", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusAccepted {
		return fmt.Errorf("registry returned status %d for blob upload", resp.StatusCode)
	}

	return nil
}

// PutManifest uploads a manifest to the registry for the given tag.
func (c *RegistryClient) PutManifest(registry, repo, tag string, manifest []byte, mediaType string) error {
	url := fmt.Sprintf("https://%s/v2/%s/manifests/%s", registry, repo, tag)

	resp, err := c.registryRequest("PUT", registry, repo, url, mediaType, strings.NewReader(string(manifest)))
	if err != nil {
		return fmt.Errorf("failed to put manifest: %w", err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		return fmt.Errorf("registry returned status %d for manifest put", resp.StatusCode)
	}

	return nil
}

func isManifestList(mediaType string) bool {
	return mediaType == "application/vnd.docker.distribution.manifest.list.v2+json" ||
		mediaType == "application/vnd.oci.image.index.v1+json"
}

func isGzipLayer(mediaType string) bool {
	return strings.Contains(mediaType, "gzip")
}

// gzipReadCloser wraps a gzip reader and closes both it and the underlying stream
type gzipReadCloser struct {
	gz         *gzip.Reader
	underlying io.ReadCloser
}

func (g *gzipReadCloser) Read(p []byte) (int, error) {
	return g.gz.Read(p)
}

func (g *gzipReadCloser) Close() error {
	g.gz.Close()
	return g.underlying.Close()
}

// SearchResult represents a single image result from a registry search.
type SearchResult struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Stars       int    `json:"star_count"`
	Official    bool   `json:"is_official"`
	Automated   bool   `json:"is_automated"`
}

// dockerHubSearchResponse is the Docker Hub v1 search API response.
type dockerHubSearchResponse struct {
	NumResults int            `json:"num_results"`
	Results    []SearchResult `json:"results"`
}

// SearchImages searches a registry for images matching the given query.
// For Docker Hub, uses the v1 search API. For other registries, uses the
// OCI catalog endpoint with client-side filtering.
func (c *RegistryClient) SearchImages(registry, query string, limit int) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 25
	}

	normalized := NormalizeRegistry(registry)
	if normalized == "registry-1.docker.io" {
		return c.searchDockerHub(query, limit)
	}

	return c.searchCatalog(normalized, query, limit)
}

// searchDockerHub uses the Docker Hub search API.
func (c *RegistryClient) searchDockerHub(query string, limit int) ([]SearchResult, error) {
	url := fmt.Sprintf("https://hub.docker.com/v2/search/repositories/?query=%s&page_size=%d", query, limit)

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating search request: %w", err)
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search returned status %d", resp.StatusCode)
	}

	var searchResp dockerHubSearchResponse
	if err := json.NewDecoder(resp.Body).Decode(&searchResp); err != nil {
		return nil, fmt.Errorf("decoding search response: %w", err)
	}

	return searchResp.Results, nil
}

// searchCatalog searches a generic OCI registry using the catalog API with filtering.
func (c *RegistryClient) searchCatalog(registry, query string, limit int) ([]SearchResult, error) {
	catalogURL := fmt.Sprintf("https://%s/v2/_catalog?n=200", registry)

	req, err := http.NewRequest("GET", catalogURL, nil)
	if err != nil {
		return nil, fmt.Errorf("creating catalog request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	c.applyBasicAuth(req, registry)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("catalog request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("catalog returned status %d", resp.StatusCode)
	}

	var catalog struct {
		Repositories []string `json:"repositories"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&catalog); err != nil {
		return nil, fmt.Errorf("decoding catalog: %w", err)
	}

	queryLower := strings.ToLower(query)
	var results []SearchResult
	for _, repo := range catalog.Repositories {
		if strings.Contains(strings.ToLower(repo), queryLower) {
			results = append(results, SearchResult{
				Name: repo,
			})
			if len(results) >= limit {
				break
			}
		}
	}

	return results, nil
}

// ListTags retrieves all tags for a repository from a registry.
func (c *RegistryClient) ListTags(registry, repo string) ([]string, error) {
	normalized := NormalizeRegistry(registry)

	tagsURL := fmt.Sprintf("https://%s/v2/%s/tags/list", normalized, repo)

	body, _, err := c.registryGet(normalized, repo, tagsURL, "application/json")
	if err != nil {
		return nil, fmt.Errorf("fetching tags: %w", err)
	}
	defer body.Close()

	var tagsResp struct {
		Name string   `json:"name"`
		Tags []string `json:"tags"`
	}
	if err := json.NewDecoder(body).Decode(&tagsResp); err != nil {
		return nil, fmt.Errorf("decoding tags response: %w", err)
	}

	return tagsResp.Tags, nil
}

// applyBasicAuth adds Basic auth to a request if credentials exist for the registry.
func (c *RegistryClient) applyBasicAuth(req *http.Request, registry string) {
	if c.credStore == nil {
		return
	}
	normalized := NormalizeRegistry(registry)
	username, password, ok := c.credStore.Get(normalized)
	if !ok {
		// Also try the raw registry name
		username, password, ok = c.credStore.Get(registry)
	}
	if ok && username != "" {
		req.SetBasicAuth(username, password)
	}
}
