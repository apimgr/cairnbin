package task

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// trivyDBLayerMediaType is the OCI layer media type carrying the Trivy
// database tarball inside the trivy-db artifact.
const trivyDBLayerMediaType = "application/vnd.aquasec.trivy.db.layer.v1.tar+gzip"

// trivyDBFilename is the destination filename for the extracted layer blob.
const trivyDBFilename = "db.tar.gz"

// ociManifestAccept lists every manifest media type the registry may return for
// an artifact reference, including both index and single-manifest forms.
const ociManifestAccept = "application/vnd.oci.image.index.v1+json," +
	"application/vnd.oci.image.manifest.v1+json," +
	"application/vnd.docker.distribution.manifest.list.v2+json," +
	"application/vnd.docker.distribution.manifest.v2+json"

// ociDescriptor is a single manifest or layer entry in an OCI manifest.
type ociDescriptor struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

// ociManifest covers both the image-index form (Manifests) and the
// image-manifest form (Layers); only one of the two is populated per document.
type ociManifest struct {
	MediaType string          `json:"mediaType"`
	Manifests []ociDescriptor `json:"manifests"`
	Layers    []ociDescriptor `json:"layers"`
}

// ociRef is a parsed OCI repository reference such as
// "ghcr.io/aquasecurity/trivy-db:2".
type ociRef struct {
	Registry   string
	Repository string
	Tag        string
}

// parseOCIRef splits an OCI reference into registry, repository, and tag. The
// tag defaults to "latest" when the reference carries none, matching the
// registry API's own default.
func parseOCIRef(ref string) (ociRef, error) {
	ref = strings.TrimPrefix(strings.TrimSpace(ref), "oci://")
	if ref == "" {
		return ociRef{}, fmt.Errorf("empty reference")
	}
	host, rest, ok := strings.Cut(ref, "/")
	if !ok || host == "" || rest == "" {
		return ociRef{}, fmt.Errorf("reference %q is not registry/repository[:tag]", ref)
	}
	tag := "latest"
	if name, t, found := strings.Cut(rest, ":"); found {
		if strings.Contains(t, "/") {
			return ociRef{}, fmt.Errorf("reference %q has a malformed tag", ref)
		}
		rest, tag = name, t
	}
	if tag == "" {
		return ociRef{}, fmt.Errorf("reference %q has an empty tag", ref)
	}
	return ociRef{Registry: host, Repository: rest, Tag: tag}, nil
}

// ociClient performs anonymous pull-scoped requests against an OCI registry,
// negotiating a bearer token when the registry challenges an unauthenticated
// request. Only the read paths needed to resolve a manifest and fetch a layer
// blob are implemented.
type ociClient struct {
	http  *http.Client
	ref   ociRef
	token string
}

func newOCIClient(ref ociRef) *ociClient {
	return &ociClient{http: &http.Client{Timeout: 5 * time.Minute}, ref: ref}
}

func newOCIClientWithHTTPClient(ref ociRef, client *http.Client) *ociClient {
	if client == nil {
		return newOCIClient(ref)
	}
	return &ociClient{http: client, ref: ref}
}

// get issues a GET against the registry, transparently acquiring a pull token
// and retrying once if the registry responds with a 401 challenge. The caller
// owns closing the returned body.
func (c *ociClient) get(ctx context.Context, path, accept string) (*http.Response, error) {
	do := func() (*http.Response, error) {
		url := "https://" + c.ref.Registry + path
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, fmt.Errorf("build request %s: %w", url, err)
		}
		if accept != "" {
			req.Header.Set("Accept", accept)
		}
		if c.token != "" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		return c.http.Do(req)
	}

	resp, err := do()
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", path, err)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}

	challenge := resp.Header.Get("WWW-Authenticate")
	_ = resp.Body.Close()
	if err := c.authenticate(ctx, challenge); err != nil {
		return nil, err
	}
	resp, err = do()
	if err != nil {
		return nil, fmt.Errorf("get %s: %w", path, err)
	}
	return resp, nil
}

// authenticate exchanges the registry's Bearer challenge for a pull token. A
// registry that offers no realm cannot be authenticated anonymously.
func (c *ociClient) authenticate(ctx context.Context, challenge string) error {
	params := parseBearerChallenge(challenge)
	realm := params["realm"]
	if realm == "" {
		return fmt.Errorf("registry %s requires auth but offered no realm", c.ref.Registry)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm, nil)
	if err != nil {
		return fmt.Errorf("build token request: %w", err)
	}
	q := req.URL.Query()
	if svc := params["service"]; svc != "" {
		q.Set("service", svc)
	}
	scope := params["scope"]
	if scope == "" {
		scope = "repository:" + c.ref.Repository + ":pull"
	}
	q.Set("scope", scope)
	req.URL.RawQuery = q.Encode()

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("token request: status %d", resp.StatusCode)
	}

	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body); err != nil {
		return fmt.Errorf("decode token: %w", err)
	}
	c.token = body.Token
	if c.token == "" {
		c.token = body.AccessToken
	}
	if c.token == "" {
		return fmt.Errorf("registry %s returned an empty token", c.ref.Registry)
	}
	return nil
}

// parseBearerChallenge extracts the key="value" pairs from a WWW-Authenticate
// Bearer challenge. Unknown and malformed pairs are ignored.
func parseBearerChallenge(challenge string) map[string]string {
	params := map[string]string{}
	rest := strings.TrimSpace(challenge)
	if i := strings.IndexByte(rest, ' '); i >= 0 && strings.EqualFold(rest[:i], "bearer") {
		rest = rest[i+1:]
	}
	for _, part := range strings.Split(rest, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		params[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"`)
	}
	return params
}

// manifest fetches and decodes the manifest identified by reference, which is
// either a tag or a digest.
func (c *ociClient) manifest(ctx context.Context, reference string) (*ociManifest, error) {
	path := "/v2/" + c.ref.Repository + "/manifests/" + reference
	resp, err := c.get(ctx, path, ociManifestAccept)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("manifest %s: status %d", reference, resp.StatusCode)
	}
	var m ociManifest
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&m); err != nil {
		return nil, fmt.Errorf("decode manifest %s: %w", reference, err)
	}
	return &m, nil
}

// resolveLayer walks from the reference's manifest down to the descriptor of
// the layer carrying mediaType, following one level of image index if present.
func (c *ociClient) resolveLayer(ctx context.Context, mediaType string) (ociDescriptor, error) {
	m, err := c.manifest(ctx, c.ref.Tag)
	if err != nil {
		return ociDescriptor{}, err
	}
	if len(m.Layers) == 0 && len(m.Manifests) > 0 {
		m, err = c.manifest(ctx, m.Manifests[0].Digest)
		if err != nil {
			return ociDescriptor{}, err
		}
	}
	for _, layer := range m.Layers {
		if layer.MediaType == mediaType {
			if layer.Digest == "" {
				return ociDescriptor{}, fmt.Errorf("layer %s has no digest", mediaType)
			}
			return layer, nil
		}
	}
	return ociDescriptor{}, fmt.Errorf("no %s layer in %s/%s:%s",
		mediaType, c.ref.Registry, c.ref.Repository, c.ref.Tag)
}

// blobToFile downloads the descriptor's blob to dst atomically, verifying its
// sha256 digest before the file is put in place. A digest mismatch leaves dst
// untouched.
func (c *ociClient) blobToFile(ctx context.Context, desc ociDescriptor, dst string) error {
	algo, want, ok := strings.Cut(desc.Digest, ":")
	if !ok || algo != "sha256" || want == "" {
		return fmt.Errorf("unsupported blob digest %q", desc.Digest)
	}

	resp, err := c.get(ctx, "/v2/"+c.ref.Repository+"/blobs/"+desc.Digest, "")
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("blob %s: status %d", desc.Digest, resp.StatusCode)
	}

	tmp, err := os.CreateTemp(filepath.Dir(dst), ".dl-*")
	if err != nil {
		return fmt.Errorf("create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, sum), resp.Body)
	if err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write blob: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temp: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("empty blob body")
	}
	if desc.Size > 0 && n != desc.Size {
		return fmt.Errorf("blob size mismatch: got %d, want %d", n, desc.Size)
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != want {
		return fmt.Errorf("blob digest mismatch: got sha256:%s, want %s", got, desc.Digest)
	}
	if err := os.Chmod(tmpName, 0o640); err != nil {
		return fmt.Errorf("chmod: %w", err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// TrivyUpdate refreshes the Aqua Trivy vulnerability database in
// {dataDir}/security/trivy/. trivy-db is published as an OCI artifact rather
// than a plain file, so the tag is resolved through the registry API and the
// application/vnd.aquasec.trivy.db.layer.v1.tar+gzip layer blob is written to
// db.tar.gz. Disabled or unconfigured, it only ensures the directory exists; a
// fetch failure degrades gracefully and keeps the existing copy. PART 18 gives
// Trivy no scheduled task of its own — cve_update owns it.
func TrivyUpdate(ctx context.Context, dataDir, source string, enabled bool) error {
	return TrivyUpdateWithClient(ctx, dataDir, source, enabled, nil)
}

// TrivyUpdateWithClient performs registry and token requests through the
// shared app-owned outbound client when provided.
func TrivyUpdateWithClient(ctx context.Context, dataDir, source string, enabled bool, httpClient *http.Client) error {
	dir := filepath.Join(dataDir, "security", "trivy")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return fmt.Errorf("trivy: mkdir %s: %w", dir, err)
	}
	if !enabled || strings.TrimSpace(source) == "" {
		return nil
	}

	ref, err := parseOCIRef(source)
	if err != nil {
		log.Printf("cve_update: trivy: %v (keeping existing copy)", err)
		return nil
	}
	client := newOCIClientWithHTTPClient(ref, httpClient)
	layer, err := client.resolveLayer(ctx, trivyDBLayerMediaType)
	if err != nil {
		log.Printf("cve_update: trivy: %v (keeping existing copy)", err)
		return nil
	}
	dst := filepath.Join(dir, trivyDBFilename)
	if err := client.blobToFile(ctx, layer, dst); err != nil {
		log.Printf("cve_update: trivy: %v (keeping existing copy)", err)
		return nil
	}
	if err := os.WriteFile(filepath.Join(dir, ".last_updated"),
		[]byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o640); err != nil {
		log.Printf("cve_update: trivy: write .last_updated: %v", err)
	}
	log.Printf("cve_update: trivy: %s — updated from %s", dst, source)
	return nil
}
