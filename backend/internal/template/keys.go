package template

import "path"

// Object key layout in the artifact store. Keys are forward-slash paths; the
// store treats them as opaque object keys (object storage is a flat namespace —
// the slashes are convention, not real directories).
//
//	active-release.json                                  -> ActivePointer (mutable)
//	releases/<release-id>/manifest.json                  -> Manifest (immutable)
//	releases/<release-id>/<size>/snap|mem|writable-seed.ext4  -> artifacts (immutable)
const (
	activePointerKey = "active-release.json"
	releasesPrefix   = "releases"
	manifestName     = "manifest.json"
)

// ActiveKey is the object key of the active-release pointer.
func ActiveKey() string { return activePointerKey }

// ReleasePrefix is the key prefix under which a release's objects live.
func ReleasePrefix(releaseID string) string {
	return path.Join(releasesPrefix, releaseID)
}

// ManifestKey is the object key of a release's manifest.
func ManifestKey(releaseID string) string {
	return path.Join(releasesPrefix, releaseID, manifestName)
}

// ArtifactKey is the object key of one variant artifact (name is an Artifact* const).
func ArtifactKey(releaseID, size, name string) string {
	return path.Join(releasesPrefix, releaseID, size, name)
}
