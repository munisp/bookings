// Package keycloak implements a thin Keycloak Admin REST client.
// It obtains an access token via the client_credentials grant and uses it to
// manage tenant groups (/tenants/{slug}) and invited users.
package keycloak

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Client is a Keycloak Admin REST client scoped to one realm.
type Client struct {
	baseURL      string
	realm        string
	clientID     string
	clientSecret string
	hc           *http.Client

	mu        sync.Mutex
	token     string
	tokenExpr time.Time
}

// New constructs the client.
func New(baseURL, realm, clientID, clientSecret string) *Client {
	return &Client{
		baseURL:      strings.TrimRight(baseURL, "/"),
		realm:        realm,
		clientID:     clientID,
		clientSecret: clientSecret,
		hc:           &http.Client{Timeout: 15 * time.Second},
	}
}

// accessToken returns a cached client_credentials token, refreshing it when
// expired (with a 30s safety margin).
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.token != "" && time.Now().Before(c.tokenExpr) {
		return c.token, nil
	}
	form := url.Values{
		"grant_type":    {"client_credentials"},
		"client_id":     {c.clientID},
		"client_secret": {c.clientSecret},
	}
	u := fmt.Sprintf("%s/realms/%s/protocol/openid-connect/token", c.baseURL, c.realm)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("keycloak token request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("keycloak token: status %d: %s", resp.StatusCode, string(b))
	}
	var tok struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&tok); err != nil {
		return "", fmt.Errorf("decode token response: %w", err)
	}
	c.token = tok.AccessToken
	c.tokenExpr = time.Now().Add(time.Duration(tok.ExpiresIn-30) * time.Second)
	return c.token, nil
}

// do performs an authenticated admin request and returns the response.
func (c *Client) do(ctx context.Context, method, path string, body any) (*http.Response, error) {
	tok, err := c.accessToken(ctx)
	if err != nil {
		return nil, err
	}
	var rdr io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rdr = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rdr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.hc.Do(req)
}

type groupRep struct {
	Name string `json:"name"`
	Path string `json:"path,omitempty"`
}

// CreateTenantGroup ensures the group path /tenants/{slug} exists (SPEC §8:
// groups map to tenants). It creates the parent "tenants" group when missing.
// Returns the leaf group's ID.
func (c *Client) CreateTenantGroup(ctx context.Context, slug string) (string, error) {
	adminBase := "/admin/realms/" + c.realm

	// find-or-create the "tenants" top-level group
	tenantsID, err := c.findGroupByPath(ctx, "/tenants")
	if err != nil {
		return "", err
	}
	if tenantsID == "" {
		resp, err := c.do(ctx, http.MethodPost, adminBase+"/groups", groupRep{Name: "tenants"})
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			return "", fmt.Errorf("create tenants group: status %d: %s", resp.StatusCode, string(b))
		}
		tenantsID, err = c.findGroupByPath(ctx, "/tenants")
		if err != nil || tenantsID == "" {
			return "", fmt.Errorf("tenants group lookup after create failed: %v", err)
		}
	}

	// find-or-create /tenants/{slug}
	path := "/tenants/" + slug
	id, err := c.findGroupByPath(ctx, path)
	if err != nil {
		return "", err
	}
	if id != "" {
		return id, nil
	}
	resp, err := c.do(ctx, http.MethodPost, adminBase+"/groups/"+tenantsID+"/children", groupRep{Name: slug})
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("create tenant group %s: status %d: %s", path, resp.StatusCode, string(b))
	}
	id, err = c.findGroupByPath(ctx, path)
	if err != nil || id == "" {
		return "", fmt.Errorf("tenant group lookup after create failed: %v", err)
	}
	return id, nil
}

// findGroupByPath returns the group ID for an exact path or "" if absent.
func (c *Client) findGroupByPath(ctx context.Context, path string) (string, error) {
	resp, err := c.do(ctx, http.MethodGet, "/admin/realms/"+c.realm+"/group-by-path/"+path, nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", nil
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("group-by-path %s: status %d: %s", path, resp.StatusCode, string(b))
	}
	var g struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&g); err != nil {
		return "", fmt.Errorf("decode group: %w", err)
	}
	return g.ID, nil
}

// CreateUserInput describes an invited member.
type CreateUserInput struct {
	Email     string
	FirstName string
	LastName  string
}

// CreateUser creates a Keycloak user with a temporary password-less invite
// (UPDATE_PASSWORD required action sends setup email flows in the realm) and
// adds the user to the tenant group /tenants/{slug}. Returns the user ID.
func (c *Client) CreateUser(ctx context.Context, slug string, in CreateUserInput) (string, error) {
	adminBase := "/admin/realms/" + c.realm
	rep := map[string]any{
		"username":        in.Email,
		"email":           in.Email,
		"firstName":       in.FirstName,
		"lastName":        in.LastName,
		"enabled":         true,
		"emailVerified":   false,
		"requiredActions": []string{"UPDATE_PASSWORD", "VERIFY_EMAIL"},
	}
	resp, err := c.do(ctx, http.MethodPost, adminBase+"/users", rep)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return "", fmt.Errorf("user %s already exists", in.Email)
	}
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return "", fmt.Errorf("create user: status %d: %s", resp.StatusCode, string(b))
	}
	loc := resp.Header.Get("Location")
	userID := loc[strings.LastIndex(loc, "/")+1:]
	if userID == "" {
		return "", fmt.Errorf("create user: missing Location header")
	}

	groupID, err := c.findGroupByPath(ctx, "/tenants/"+slug)
	if err != nil {
		return "", err
	}
	if groupID == "" {
		return "", fmt.Errorf("tenant group /tenants/%s not found", slug)
	}
	jresp, err := c.do(ctx, http.MethodPut, adminBase+"/users/"+userID+"/groups/"+groupID, nil)
	if err != nil {
		return "", err
	}
	defer jresp.Body.Close()
	if jresp.StatusCode != http.StatusNoContent {
		b, _ := io.ReadAll(io.LimitReader(jresp.Body, 512))
		return "", fmt.Errorf("join group: status %d: %s", jresp.StatusCode, string(b))
	}
	return userID, nil
}
