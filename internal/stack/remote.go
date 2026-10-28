package stack

import (
	"context"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// RequestActor is transport attribution, never an authorization principal.
type RequestActor struct {
	Login string `json:"login"`
	Name  string `json:"name"`
}
type requestActorKey struct{}

func ActorFromContext(ctx context.Context) (RequestActor, bool) {
	a, ok := ctx.Value(requestActorKey{}).(RequestActor)
	return a, ok
}

var serveName = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)+\.ts\.net$`)

// SetServeHost allows exactly this HTTPS name. It may be set after Serve starts.
func (s *Service) SetServeHost(host string) error {
	if host != "" && (len(host) > 253 || !serveName.MatchString(host)) {
		return failure("invalid_input", "Serve requires the node's MagicDNS ts.net name")
	}
	s.serveHost.Store(host)
	return nil
}

func (s *Service) SharedURL() string {
	host, _ := s.serveHost.Load().(string)
	if host == "" {
		return ""
	}
	return "https://" + host
}

func (s *Service) serveRequest(r *http.Request) bool {
	host, _ := s.serveHost.Load().(string)
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	return host != "" && r.Host == host && err == nil && net.ParseIP(ip).IsLoopback()
}

func (s *Service) prepareRequest(r *http.Request) (*http.Request, bool, error) {
	shared := s.serveRequest(r)
	if !shared && r.Host != strings.TrimPrefix(s.Discovery.URL, "http://") {
		return r, false, failure("forbidden", "invalid Host")
	}
	originURL := s.Discovery.URL
	if shared {
		originURL = "https://" + r.Host
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != originURL {
		return r, false, failure("forbidden", "cross-origin requests are forbidden")
	}
	if r.URL.RawQuery != "" || r.URL.User != nil {
		return r, false, failure("invalid_input", "query parameters are not supported")
	}
	if r.Header.Get("Tailscale-Funnel-Request") != "" {
		return r, false, failure("forbidden", "Tailscale Funnel requests are not supported")
	}
	if shared {
		r = requestWithActor(r)
	}
	return r, shared, nil
}

func requestWithActor(r *http.Request) *http.Request {
	login, name := r.Header.Values("Tailscale-User-Login"), r.Header.Values("Tailscale-User-Name")
	if len(login) != 1 || login[0] == "" || len(login[0]) > 2048 || len(name) > 1 {
		return r
	}
	// Serve Q-encodes non-ASCII login and display names (RFC 2047).
	decoder := new(mime.WordDecoder)
	decoded, err := decoder.DecodeHeader(login[0])
	if err != nil {
		return r
	}
	a := RequestActor{Login: decoded}
	if len(name) == 1 {
		if len(name[0]) > 2048 {
			return r
		}
		a.Name, err = decoder.DecodeHeader(name[0])
		if err != nil {
			return r
		}
	}
	if a.Login == "" || len(a.Login) > 320 || len(a.Name) > 256 || !utf8.ValidString(a.Login+a.Name) || strings.ContainsFunc(a.Login+a.Name, func(r rune) bool { return r < 32 || r == 127 }) {
		return r
	}
	return r.WithContext(context.WithValue(r.Context(), requestActorKey{}, a))
}

type RemoteProfile struct {
	Name       string `json:"name"`
	URL        string `json:"url"`
	DatabaseID string `json:"database_id"`
	Version    string `json:"version"`
}

type RemoteHealth struct {
	DatabaseID string `json:"database_id"`
	InstanceID string `json:"instance_id"`
	Version    string `json:"version"`
}

var remoteName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

func remoteDirectory() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil || !filepath.IsAbs(dir) {
		return "", failure("invalid_input", "cannot find an absolute config directory")
	}
	return filepath.Join(dir, "a-stack", "remotes"), nil
}

func remotePath(name string) (string, error) {
	if !remoteName.MatchString(name) || name == "list" || name == "remove" {
		return "", failure("invalid_input", "remote name must be 1–64 letters, digits, underscores or hyphens; list and remove are reserved")
	}
	dir, err := remoteDirectory()
	return filepath.Join(dir, name+".json"), err
}

func remoteURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if len(raw) > 2048 || err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || strings.Contains(raw, "#") {
		return "", failure("invalid_input", "remote URL must be an HTTPS origin, without credentials, path, query or fragment")
	}
	u.Path = ""
	return u.String(), nil
}

func remoteHTTP() *http.Client {
	return &http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext, DisableKeepAlives: true}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func discoverRemote(ctx context.Context, client *http.Client, origin string) (RemoteHealth, error) {
	var envelope struct {
		Data RemoteHealth `json:"data"`
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/v1/health", nil)
	if err != nil {
		return envelope.Data, failure("invalid_input", "invalid remote URL")
	}
	resp, err := client.Do(req)
	if err != nil {
		return envelope.Data, unreachable("cannot reach remote a-stack over HTTPS; check the URL, Tailscale connection and certificate")
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if err != nil || len(data) > 4096 || resp.StatusCode != http.StatusOK || decodeJSON(data, &envelope) != nil || !validToken(envelope.Data.DatabaseID) || !validToken(envelope.Data.InstanceID) || envelope.Data.Version == "" || len(envelope.Data.Version) > 128 {
		return envelope.Data, failure("invalid_input", "remote does not provide compatible a-stack health discovery")
	}
	return envelope.Data, nil
}

func ConnectRemote(ctx context.Context, name, rawURL string) (RemoteProfile, error) {
	return connectRemote(ctx, name, rawURL, remoteHTTP())
}

func connectRemote(ctx context.Context, name, rawURL string, client *http.Client) (RemoteProfile, error) {
	var p RemoteProfile
	path, err := remotePath(name)
	if err != nil {
		return p, err
	}
	origin, err := remoteURL(rawURL)
	if err != nil {
		return p, err
	}
	h, err := discoverRemote(ctx, client, origin)
	if err != nil {
		return p, err
	}
	p = RemoteProfile{name, origin, h.DatabaseID, h.Version}
	if err = MkdirPrivate(filepath.Dir(path)); err != nil {
		return p, err
	}
	if err = checkPrivateDirectory(filepath.Dir(path)); err != nil {
		return p, err
	}
	// A name never silently switches databases or overwrites another profile.
	return p, writePrivate(path, p, false)
}

func ReadRemote(name string) (RemoteProfile, error) {
	var p RemoteProfile
	path, err := remotePath(name)
	if err != nil {
		return p, err
	}
	b, err := privateRead(path)
	if err != nil {
		return p, err
	}
	if decodeJSON(b, &p) != nil || p.Name != name || !validToken(p.DatabaseID) {
		return p, failure("invalid_input", "invalid remote profile")
	}
	if _, err = remoteURL(p.URL); err != nil {
		return p, err
	}
	return p, nil
}

func ListRemotes() ([]RemoteProfile, error) {
	profiles := []RemoteProfile{}
	dir, err := remoteDirectory()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return profiles, nil
	}
	if err != nil {
		return nil, storageError(err)
	}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		p, err := ReadRemote(strings.TrimSuffix(entry.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		profiles = append(profiles, p)
	}
	return profiles, nil
}

func RemoveRemote(name string) error {
	path, err := remotePath(name)
	if err != nil {
		return err
	}
	if err = os.Remove(path); err != nil {
		return failure("invalid_input", "remote profile is missing or inaccessible")
	}
	return syncDir(filepath.Dir(path))
}

// NewRemoteClient discovers each new instance, pins the database and never falls
// back to a local Operator credential. The token is sent only after discovery.
func NewRemoteClient(ctx context.Context, name, credentialFile string, kind CapabilityKind) (*Client, error) {
	return newRemoteClient(ctx, name, credentialFile, kind, remoteHTTP())
}

func newRemoteClient(ctx context.Context, name, credentialFile string, kind CapabilityKind, client *http.Client) (*Client, error) {
	if credentialFile == "" {
		return nil, failure("invalid_input", "--remote requires an explicit --credential-file for the remote service")
	}
	if kind != "" && !validKind(kind) {
		return nil, failure("invalid_input", "invalid credential kind")
	}
	p, err := ReadRemote(name)
	if err != nil {
		return nil, err
	}
	c, err := ReadCredential(credentialFile)
	if err != nil {
		return nil, err
	}
	if kind != "" && c.Kind != kind {
		return nil, failure("forbidden", "credential file has a different capability kind")
	}
	h, err := discoverRemote(ctx, client, p.URL)
	if err != nil {
		return nil, err
	}
	if h.DatabaseID != p.DatabaseID {
		return nil, failure("conflict", "remote database changed; remove and reconnect the profile after checking the server")
	}
	return &Client{RemoteName: name, Discovery: Discovery{p.URL, h.DatabaseID, h.InstanceID}, Credential: c, HTTP: client}, nil
}

// Keep health discovery separate from the authenticated local health response.
func (s *Service) remoteHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		writeError(w, failure("invalid_input", "GET without a body required"))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"data": RemoteHealth{s.Store.id, s.Discovery.InstanceID, s.Version}})
}
