// Package version is birddog's single version, shared by the binary and the
// opencode plugin.
//
// It is kept in step with plugins/opencode/package.json by
// scripts/sync-version.mjs, and a test asserts they agree — a plugin whose
// version has drifted from the binary reading it is the failure that
// versioning exists to prevent.
package version

// Version is the current release.
const Version = "1.0.1"
