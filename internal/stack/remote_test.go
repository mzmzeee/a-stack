package stack

import (
	"context"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServeHostOriginAndActor(t *testing.T) {
	s, _, cred := startTest(t)
	host := "server.example.ts.net"
	if err := s.SetServeHost(host); err != nil {
		t.Fatal(err)
	}

	for _, tc := range []struct {
		host, origin, remote string
		want                 int
	}{
		{host, "https://" + host, "127.0.0.1:1234", 200},
		{host, "http://" + host, "127.0.0.1:1234", 403},
		{host, s.Discovery.URL, "127.0.0.1:1234", 403},
		{host, "", "100.100.1.2:1234", 403},
		{"evil.ts.net", "", "127.0.0.1:1234", 403},
		{host + ":443", "", "127.0.0.1:1234", 403},
		{strings.TrimPrefix(s.Discovery.URL, "http://"), s.Discovery.URL, "127.0.0.1:1234", 200},
	} {
		t.Run(tc.host+tc.origin+tc.remote, func(t *testing.T) {
			r := httptest.NewRequest("GET", "http://"+tc.host+"/v1/health", nil)
			r.RemoteAddr = tc.remote
			r.Header.Set("Origin", tc.origin)
			r.Header.Set("Authorization", "Bearer "+cred.Token)
			r.Header.Set("X-A-Stack-Instance", s.Discovery.InstanceID)
			r.Header.Set("Tailscale-User-Login", "alice@example.com")
			r.Header.Set("Tailscale-User-Name", "Alice")
			w := httptest.NewRecorder()
			s.dispatch(w, r)

			if w.Code != tc.want {
				t.Fatalf("status %d: %s", w.Code, w.Body.String())
			}

			prepared, shared, err := s.prepareRequest(r)
			if shared && err == nil {
				actor, ok := ActorFromContext(prepared.Context())
				if !ok || actor.Login != "alice@example.com" || actor.Name != "Alice" {
					t.Fatalf("actor %+v %v", actor, ok)
				}
			} else if _, ok := ActorFromContext(prepared.Context()); ok {
				t.Fatal("untrusted request got actor")
			}
		})
	}

	for _, bad := range []string{"evil.com", "*.ts.net", "https://server.example.ts.net", "server.example.ts.net:443"} {
		if err := s.SetServeHost(bad); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}

	funnel := httptest.NewRequest("GET", "https://"+host+"/v1/health", nil)
	funnel.RemoteAddr = "127.0.0.1:1234"
	funnel.Header.Set("Tailscale-Funnel-Request", "?1")

	if _, _, err := s.prepareRequest(funnel); err == nil {
		t.Fatal("accepted Funnel traffic")
	}

	funnel.Host = strings.TrimPrefix(s.Discovery.URL, "http://")
	if _, _, err := s.prepareRequest(funnel); err == nil {
		t.Fatal("accepted Funnel with loopback Host")
	}

	for _, login := range []string{"", "=?utf-8?q?bad=0Alogin?=", strings.Repeat("x", 321)} {
		r := httptest.NewRequest("GET", "https://"+host+"/v1/health", nil)
		r.Header.Set("Tailscale-User-Login", login)

		if _, ok := ActorFromContext(requestWithActor(r).Context()); ok {
			t.Fatalf("accepted invalid login %q", login)
		}
	}

	for _, name := range []string{"معتز", "Renée"} {
		r := httptest.NewRequest("GET", "https://"+host+"/v1/health", nil)
		r.Header.Set("Tailscale-User-Login", "alice@example.com")
		r.Header.Set("Tailscale-User-Name", mime.QEncoding.Encode("utf-8", name))

		a, ok := ActorFromContext(requestWithActor(r).Context())
		if !ok || a.Name != name {
			t.Fatalf("encoded name: %+v %v", a, ok)
		}
	}

	r := httptest.NewRequest("GET", "https://"+host+"/v1/health", nil)
	r.Header.Add("Tailscale-User-Login", "alice")
	r.Header.Add("Tailscale-User-Login", "mallory")

	if _, ok := ActorFromContext(requestWithActor(r).Context()); ok {
		t.Fatal("duplicate identity accepted")
	}
}

func TestRemoteTLSProxyAndProfiles(t *testing.T) {
	t.Setenv("HOME", privateTestDir(t))
	t.Setenv("XDG_CONFIG_HOME", privateTestDir(t))
	// os.UserConfigDir uses AppData on Windows.
	t.Setenv("AppData", os.Getenv("XDG_CONFIG_HOME"))
	s, dir, _ := startTest(t)

	s.Version = "v0.2.0"
	const host = "server.example.ts.net"
	if err := s.SetServeHost(host); err != nil {
		t.Fatal(err)
	}

	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prepared, shared, err := s.prepareRequest(r)
		if err != nil || !shared {
			t.Errorf("proxy boundary: shared=%v err=%v", shared, err)
		}

		actor, ok := ActorFromContext(prepared.Context())
		if !ok || actor.Login != "alice@example.com" || actor.Name != "Alice" {
			t.Errorf("proxy actor: %+v %v", actor, ok)
		}

		s.dispatch(w, r)
	}))
	defer backend.Close()
	target, _ := url.Parse(backend.URL)
	proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(target)
		r.Out.Host = host
		r.Out.Header.Del("Tailscale-User-Login")
		r.Out.Header.Del("Tailscale-User-Name")
		r.Out.Header.Set("Tailscale-User-Login", "alice@example.com")
		r.Out.Header.Set("Tailscale-User-Name", "Alice")
	}}
	tls := httptest.NewTLSServer(proxy)
	defer tls.Close()
	trusted := tls.Client()
	trusted.CheckRedirect = remoteHTTP().CheckRedirect

	profile, err := connectRemote(ctx, "team", tls.URL, trusted)
	if err != nil {
		t.Fatal(err)
	}

	if profile.DatabaseID != s.Store.id || profile.Version != "v0.2.0" {
		t.Fatalf("profile %+v", profile)
	}

	if _, err = ConnectRemote(ctx, "untrusted", tls.URL); err == nil {
		t.Fatal("untrusted TLS certificate accepted")
	}

	profiles, err := ListRemotes()
	if err != nil || len(profiles) != 1 {
		t.Fatalf("list %+v %v", profiles, err)
	}

	if _, err = connectRemote(ctx, "team", tls.URL, trusted); err == nil {
		t.Fatal("overwrote existing profile")
	}

	client, err := newRemoteClient(ctx, "team", filepath.Join(dir, "operator.json"), OperatorCapability, trusted)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = client.Do(ctx, "GET", "/v1/storage", nil); err != nil {
		t.Fatal(err)
	}

	// Serve strips caller-supplied identity and sets the authenticated caller.
	req, _ := http.NewRequest("GET", tls.URL+"/v1/health", nil)
	req.Header.Set("Tailscale-User-Login", "mallory@example.com")

	response, err := trusted.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	response.Body.Close()

	if response.StatusCode != 200 {
		t.Fatal(response.StatusCode)
	}

	// Public discovery is limited to the configured proxy host, not local health.
	req, _ = http.NewRequest("GET", s.Discovery.URL+"/v1/health", nil)

	response, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	response.Body.Close()

	if response.StatusCode == 200 {
		t.Fatal("loopback health unexpectedly public")
	}

	// The remote path still requires a bearer for any data endpoint.
	response, err = trusted.Get(tls.URL + "/v1/storage")
	if err != nil {
		t.Fatal(err)
	}

	response.Body.Close()

	if response.StatusCode == 200 {
		t.Fatal("remote storage unexpectedly public")
	}

	// A restored/replaced database is detected before a bearer is sent.
	profile.DatabaseID = opaqueID()
	path, _ := remotePath("team")
	if err = writePrivate(path, profile, true); err != nil {
		t.Fatal(err)
	}

	_, err = newRemoteClient(ctx, "team", filepath.Join(dir, "operator.json"), OperatorCapability, trusted)
	requireCode(t, err, "conflict")

	if _, err = newRemoteClient(ctx, "team", "", OperatorCapability, trusted); err == nil {
		t.Fatal("allowed implicit local credential")
	}

	if err = RemoveRemote("team"); err != nil {
		t.Fatal(err)
	}

	profiles, err = ListRemotes()
	if err != nil || len(profiles) != 0 {
		t.Fatalf("remove %+v %v", profiles, err)
	}
}

func TestRemoteValidationAndDiscoveryFailures(t *testing.T) {
	for _, raw := range []string{"https://" + strings.Repeat("x", 2049), "http://server.example.ts.net", "https://user:pass@server.example.ts.net", "https://server.example.ts.net/path", "https://server.example.ts.net?", "https://server.example.ts.net#", "https://server.example.ts.net?x=y", "https://server.example.ts.net/#secret"} {
		if _, err := remoteURL(raw); err == nil {
			t.Fatalf("accepted URL %q", raw)
		}
	}

	for _, name := range []string{"../escape", "/absolute", "list", "remove", "", strings.Repeat("a", 65)} {
		if _, err := remotePath(name); err == nil {
			t.Fatalf("accepted name %q", name)
		}
	}

	for _, tc := range []struct {
		status int
		body   string
	}{{302, ""}, {200, `{"data":{}}`}, {200, strings.Repeat("x", 4097)}, {403, `{"error":{}}`}} {
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "" {
				t.Error("discovery leaked bearer")
			}

			w.WriteHeader(tc.status)
			_, _ = io.WriteString(w, tc.body)
		}))
		_, err := discoverRemote(context.Background(), server.Client(), server.URL)
		server.Close()

		if err == nil {
			t.Fatalf("accepted %+v", tc)
		}
	}

	if remoteHTTP().Transport.(*http.Transport).Proxy != nil {
		t.Fatal("remote trusts proxy environment")
	}

	if remoteHTTP().CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Fatal("remote follows redirects")
	}
}
