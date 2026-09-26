package version

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// The plugin ships separately from the binary that reads its records, so a
// version that drifts is invisible until something misbehaves in the field.
// This fails the build instead.
func TestVersionMatchesThePublishedPlugin(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "plugins", "opencode", "package.json"))
	if err != nil {
		t.Fatalf("read plugin package.json: %v", err)
	}
	var pkg struct {
		Name    string `json:"name"`
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		t.Fatalf("parse plugin package.json: %v", err)
	}
	if pkg.Version != Version {
		t.Errorf("%s is at %s but the binary is at %s; run: npm run sync-version",
			pkg.Name, pkg.Version, Version)
	}
}

func TestVersionMatchesTheRootPackage(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "package.json"))
	if err != nil {
		t.Fatalf("read root package.json: %v", err)
	}
	var pkg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		t.Fatalf("parse root package.json: %v", err)
	}
	if pkg.Version != Version {
		t.Errorf("root package.json is at %s but the binary is at %s; run: npm run sync-version",
			pkg.Version, Version)
	}
}

func TestVersionLooksLikeARelease(t *testing.T) {
	if !regexp.MustCompile(`^\d+\.\d+\.\d+$`).MatchString(Version) {
		t.Errorf("Version = %q, want a plain semver the release tag can match", Version)
	}
}
