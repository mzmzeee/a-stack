package stack

import "testing"

func TestRepositoryIdentity(t *testing.T) {
	for _, remote := range []string{
		"git@EXAMPLE.com:team/repo.git", "https://example.com/team/repo.git",
		"ssh://git@example.com:22/team/repo", "https://example.com:443/team/repo/",
		"https://user:secret@example.com/team/repo.git", "git://example.com:9418/team/repo.git",
	} {
		got, err := RepositoryIdentity(remote)
		if err != nil || got != "example.com/team/repo" {
			t.Errorf("network remote normalization: got %q, %v", got, err)
		}
	}

	for _, tc := range []struct{ remote, want string }{
		{"ssh://git@example.com:2222/Team/Repo.git", "example.com:2222/Team/Repo"},
		{"ssh://git@[::1]/team/repo.git", "[::1]/team/repo"},
	} {
		got, err := RepositoryIdentity(tc.remote)
		if err != nil || got != tc.want {
			t.Errorf("identity preservation: got %q, %v; want %q", got, err, tc.want)
		}
	}

	for _, remote := range []string{
		"", "/tmp/repo", "../repo", "C:\\repo", "file:///tmp/repo", "example.com:",
		"https://example.com/", "https://example.com/team/../repo", "https://example.com/team/%2e%2e/repo",
		"https://example.com/team//repo", "https://example.com/team/repo?token=secret",
		"https://example.com/team/repo#secret", "ssh://host/team/repo\n", "https://example.com/team/%00repo",
	} {
		if _, err := RepositoryIdentity(remote); err == nil {
			t.Error("accepted invalid repository remote")
		}
	}
}
