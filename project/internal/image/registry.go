// Package image provides the OCI registry client for pulling container images.
package image

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// RegistryClient implements the OCI Distribution Spec for pulling images
// from Docker Hub, GCR, ECR, GHCR, and any OCI-compliant registry.
type RegistryClient struct {
	httpClient *http.Client
	tokens     map[string]tokenEntry
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

// DownloadLayer streams a layer blob from the registry.
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

// registryGet performs an authenticated GET to a registry endpoint.
// It handles the WWW-Authenticate challenge flow for bearer token auth.
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

		token, expiresIn, err := c.fetchToken(challenge, repo)
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
func (c *RegistryClient) fetchToken(challenge, repo string) (string, int, error) {
	params := parseWWWAuthenticate(challenge)

	realm := params["realm"]
	if realm == "" {
		return "", 0, fmt.Errorf("no realm in WWW-Authenticate header")
	}

	// Build token request URL
	tokenURL := realm + "?"
	if svc, ok := params["service"]; ok {
		tokenURL += "service=" + svc + "&"
	}
	if scope, ok := params["scope"]; ok {
		tokenURL += "scope=" + scope
	} else {
		tokenURL += "scope=repository:" + repo + ":pull"
	}

	resp, err := c.httpClient.Get(tokenURL)
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

	// Preference order
	preferences := []struct{ arch, variant string }{
		{"amd64", ""},
		{"arm64", ""},
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

		token, expiresIn, err := c.fetchTokenWithScope(challenge, repo, "push,pull")
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

	return resp, nil
}

// fetchTokenWithScope performs token exchange with a specific scope (e.g., "push,pull").
func (c *RegistryClient) fetchTokenWithScope(challenge, repo, scope string) (string, int, error) {
	params := parseWWWAuthenticate(challenge)

	realm := params["realm"]
	if realm == "" {
		return "", 0, fmt.Errorf("no realm in WWW-Authenticate header")
	}

	tokenURL := realm + "?"
	if svc, ok := params["service"]; ok {
		tokenURL += "service=" + svc + "&"
	}
	tokenURL += "scope=repository:" + repo + ":" + scope

	resp, err := c.httpClient.Get(tokenURL)
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
