package stack

import (
	"net"
	"net/url"
	"strings"
)

// RepositoryIdentity matches network Git remotes without retaining credentials.
// Paths remain case-sensitive; different repositories must never share a Workspace.
func RepositoryIdentity(remote string) (string, error) {
	invalid := func() (string, error) {
		return "", failure("invalid_input", "sharing requires a network Git remote with a host and repository path")
	}
	if remote == "" || len(remote) > 4096 || strings.ContainsAny(remote, "\x00\r\n\t ") {
		return invalid()
	}

	if !strings.Contains(remote, "://") {
		// Git's scp form: [user@]host:path. Absolute and relative local
		// paths have no stable identity on another machine.
		host, path, ok := strings.Cut(remote, ":")
		if !ok || len(host) < 2 || strings.ContainsAny(host, "/\\") || strings.HasPrefix(path, "/") {
			return invalid()
		}

		remote = "ssh://" + host + "/" + path
	}

	u, err := url.Parse(remote)
	if err != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" {
		return invalid()
	}

	switch u.Scheme {
	case "ssh", "https", "http", "git":
	default:
		return invalid()
	}

	host := strings.ToLower(u.Hostname())
	if host == "" || strings.ContainsAny(host, "\\%@") {
		return invalid()
	}

	port := u.Port()
	if (u.Scheme == "ssh" && port == "22") || (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") || (u.Scheme == "git" && port == "9418") {
		port = ""
	}

	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}

	path := strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), ".git")
	path = strings.TrimPrefix(path, "/")
	if path == "" || strings.ContainsAny(path, "\\\x00\r\n\t") {
		return invalid()
	}

	for _, part := range strings.Split(path, "/") {
		if part == "" || part == "." || part == ".." {
			return invalid()
		}
	}

	return host + "/" + path, nil
}
