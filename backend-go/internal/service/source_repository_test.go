package service

import (
	"strings"
	"testing"
)

func TestSourceRepositoryIdentityNormalizesCommonGitRemotes(t *testing.T) {
	for _, raw := range []string{
		"https://CodeUp.Aliyun.com/team/app.git",
		"ssh://git@codeup.aliyun.com:22/team/app.git",
		"git@codeup.aliyun.com:team/app.git",
		"git://codeup.aliyun.com/team/app",
	} {
		display, identity, err := sourceRepositoryIdentity(raw)
		if err != nil {
			t.Errorf("sourceRepositoryIdentity(%q) error = %v", raw, err)
			continue
		}
		if display != "codeup.aliyun.com/team/app" || identity != display {
			t.Errorf("sourceRepositoryIdentity(%q) = (%q, %q), want canonical identity", raw, display, identity)
		}
	}
}

func TestSourceRepositoryIdentityRejectsCredentialsAndAmbiguousURLs(t *testing.T) {
	invalidRepositories := []string{
		"https://user:secret@codeup.aliyun.com/team/app.git",
		"ssh://git:secret@codeup.aliyun.com/team/app.git",
		"https://codeup.aliyun.com/team/app.git?token=secret",
		"git@codeup.aliyun.com:team/app?token=secret",
		"https://codeup.aliyun.com/team/repo%20name.git",
		"https://codeup.aliyun.com:bad/team/app.git",
		"git@codeup.aliyun.com",
		"https://codeup.aliyun.com/" + strings.Repeat("a", 1025),
	}
	for _, raw := range invalidRepositories {
		if _, _, err := sourceRepositoryIdentity(raw); err == nil {
			t.Errorf("sourceRepositoryIdentity(%q) accepted an ambiguous or credentialed URL", raw)
		}
	}
}

func TestNormalizeSourceRepositoriesDeduplicatesHTTPSAndSSHForms(t *testing.T) {
	got, identities, err := normalizeSourceRepositories([]string{
		"https://codeup.aliyun.com/team/app.git",
		"git@codeup.aliyun.com:team/app.git",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "codeup.aliyun.com/team/app" {
		t.Fatalf("normalized repositories = %#v", got)
	}
	if len(identities) != 1 {
		t.Fatalf("repository identities = %#v", identities)
	}
}

func TestNormalizeBuildSourcePathEnforcesRepositoryRelativeDirectory(t *testing.T) {
	for _, path := range []string{"", ".", "services/api", "apps/shop/api"} {
		got, err := normalizeBuildSourcePath(path)
		if err != nil || got != path {
			t.Errorf("normalizeBuildSourcePath(%q) = (%q, %v)", path, got, err)
		}
	}
	for _, path := range []string{"../outside", "services/../outside", "/services/api", "services\\api", "services//api"} {
		if _, err := normalizeBuildSourcePath(path); err == nil {
			t.Errorf("normalizeBuildSourcePath(%q) accepted traversal or malformed path", path)
		}
	}
}
