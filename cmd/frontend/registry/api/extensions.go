package api

import (
	"context"
	"net/url"
	"strings"

	"github.com/cockroachdb/errors"

	"github.com/sourcegraph/sourcegraph/cmd/frontend/globals"
	"github.com/sourcegraph/sourcegraph/schema"

	"github.com/sourcegraph/sourcegraph/cmd/frontend/envvar"
	"github.com/sourcegraph/sourcegraph/cmd/frontend/graphqlbackend"
	registry "github.com/sourcegraph/sourcegraph/cmd/frontend/registry/client"
	"github.com/sourcegraph/sourcegraph/internal/conf"
	"github.com/sourcegraph/sourcegraph/internal/database/dbutil"
	"github.com/sourcegraph/sourcegraph/internal/jsonc"
)

// SplitExtensionID splits an extension ID of the form [host/]publisher/name (where [host/] is the
// optional registry prefix), such as "alice/myextension" or
// "sourcegraph.example.com/bob/myextension". It returns the components, or a non-nil error if
// parsing failed.
func SplitExtensionID(extensionID string) (prefix, publisher, name string, err error) {
	parts := strings.Split(extensionID, "/")
	if len(parts) == 0 || len(parts) == 1 {
		return "", "", "", errors.Errorf("invalid extension ID: %q (2+ slash-separated path components required)", extensionID)
	}
	name = parts[len(parts)-1] // last
	if name == "" {
		return "", "", "", errors.Errorf("invalid extension ID: %q (trailing slash is forbidden)", extensionID)
	}
	publisher = parts[len(parts)-2] // 2nd to last
	if publisher == "" {
		return "", "", "", errors.Errorf("invalid extension ID: %q (empty publisher)", extensionID)
	}
	prefix = strings.Join(parts[:len(parts)-2], "/") // prefix
	return
}

// ParseExtensionID parses an extension ID of the form [host/]publisher/name (where [host/] is the
// optional registry prefix), such as "alice/myextension" or
// "sourcegraph.example.com/bob/myextension". It validates that the registry prefix is correct given
// the current configuration.
func ParseExtensionID(extensionID string) (prefix, extensionIDWithoutPrefix string, isLocal bool, err error) {
	prefix, publisher, name, err := SplitExtensionID(extensionID)
	if err != nil {
		return "", "", false, err
	}

	configuredPrefix := GetLocalRegistryExtensionIDPrefix()
	if prefix != "" {
		// Extension ID is host/publisher/name.
		if configuredPrefix == nil {
			// Don't look up fully qualified extensions from Sourcegraph.com; it only cares about
			// its own extensions.
			return "", "", false, errors.Errorf("remote extension lookup is not supported for host %q", prefix)
		}

		// Local extension on non-Sourcegraph.com instance.
		if prefix != *configuredPrefix {
			return "", "", false, errors.Errorf("remote extension lookup is forbidden (extension ID prefix %q, allowed prefixes are \"\" (default) and %q (local))", prefix, *configuredPrefix)
		}
		isLocal = true
	} else if configuredPrefix == nil { // Extension ID is publisher/name.
		// Local extension on Sourcegraph.com instance.
		isLocal = true
	}

	extensionIDWithoutPrefix = publisher + "/" + name
	return prefix, extensionIDWithoutPrefix, isLocal, nil
}

// GetLocalExtensionByExtensionID looks up and returns the registry extension in the local registry
// with the given extension ID. If there is no local extension registry, it is not implemented.
var GetLocalExtensionByExtensionID func(ctx context.Context, db dbutil.DB, extensionIDWithoutPrefix string) (local graphqlbackend.RegistryExtension, err error)

// GetExtensionByExtensionID gets the extension with the given extension ID.
//
// It returns either a local or remote extension, depending on what the extension ID refers to.
//
// The format of an extension ID is [host/]publisher/name. If the host is omitted, the host defaults
// to the remote registry specified in site configuration (usually sourcegraph.com). The host must
// be specified to refer to a local extension on the current Sourcegraph site (e.g.,
// sourcegraph.example.com/publisher/name).
func GetExtensionByExtensionID(ctx context.Context, db dbutil.DB, extensionID string) (local graphqlbackend.RegistryExtension, remote *registry.Extension, err error) {
	_, extensionIDWithoutPrefix, isLocal, err := ParseExtensionID(extensionID)
	if err != nil {
		return nil, nil, err
	}

	if isLocal {
		if GetLocalExtensionByExtensionID != nil {
			x, err := GetLocalExtensionByExtensionID(ctx, db, extensionIDWithoutPrefix)
			return x, nil, err
		}
	}

	x, err := getRemoteRegistryExtension(ctx, "extensionID", extensionIDWithoutPrefix)
	if err != nil {
		return nil, nil, err
	}
	return nil, x, nil
}

// getLocalRegistryName returns the name of the local registry.
func getLocalRegistryName() string {
	return registry.Name(globals.ExternalURL())
}

var mockLocalRegistryExtensionIDPrefix **string

// GetLocalRegistryExtensionIDPrefix returns the extension ID prefix (if any) of extensions in the
// local registry.
func GetLocalRegistryExtensionIDPrefix() *string {
	if mockLocalRegistryExtensionIDPrefix != nil {
		return *mockLocalRegistryExtensionIDPrefix
	}
	if envvar.SourcegraphDotComMode() {
		return nil
	}
	name := getLocalRegistryName()
	return &name
}

// getRemoteRegistryURL returns the remote registry URL from site configuration, or nil if there is
// none. If an error exists while parsing the value in site configuration, the error is returned.
func getRemoteRegistryURL() (*url.URL, error) {
	pc := conf.Extensions()
	if pc == nil || pc.RemoteRegistryURL == "" {
		return nil, nil
	}
	return url.Parse(pc.RemoteRegistryURL)
}

// IsRemoteExtensionAllowed reports whether to allow usage of the remote extension with the given
// extension ID.
//
// It can be overridden to use custom logic.
var IsRemoteExtensionAllowed = func(extensionID string) bool {
	// By default, all remote extensions are allowed.
	return true
}

var mockGetRemoteRegistryExtension func(field, value string) (*registry.Extension, error)

// getRemoteRegistryExtension gets the remote registry extension and rewrites its fields to be from
// the frame-of-reference of this site. The field is either "uuid" or "extensionID".
func getRemoteRegistryExtension(ctx context.Context, field, value string) (*registry.Extension, error) {
	if mockGetRemoteRegistryExtension != nil {
		return mockGetRemoteRegistryExtension(field, value)
	}

	registryURL, err := getRemoteRegistryURL()
	if registryURL == nil || err != nil {
		return nil, err
	}

	var x *registry.Extension
	switch field {
	case "uuid":
		x, err = registry.GetByUUID(ctx, registryURL, value)
	case "extensionID":
		x, err = registry.GetByExtensionID(ctx, registryURL, value)
	default:
		panic("unexpected field: " + field)
	}
	if x != nil {
		x.RegistryURL = registryURL.String()
	}

	if x != nil && !IsRemoteExtensionAllowed(x.ExtensionID) {
		return nil, errors.Errorf("extension is not allowed in site configuration: %q", x.ExtensionID)
	}

	return x, err
}

// FilterRemoteExtensions is called to filter the list of extensions retrieved from the remote
// registry before the list is used by any other part of the application.
//
// It can be overridden to use custom logic.
var FilterRemoteExtensions = func(extensions []*registry.Extension) []*registry.Extension {
	// By default, all remote extensions are allowed.
	return extensions
}

// listRemoteRegistryExtensions lists the remote registry extensions and rewrites their fields to be
// from the frame-of-reference of this site.
func listRemoteRegistryExtensions(ctx context.Context, query string) ([]*registry.Extension, error) {
	registryURL, err := getRemoteRegistryURL()
	if registryURL == nil || err != nil {
		return nil, err
	}

	xs, err := registry.List(ctx, registryURL, query)
	if err != nil {
		return nil, err
	}
	xs = FilterRemoteExtensions(xs)
	for _, x := range xs {
		x.RegistryURL = registryURL.String()
	}
	return xs, nil
}

// GetLocalFeaturedExtensions looks up and returns the featured registry extensions in the local registry
// If this is not sourcegraph.com, it is not implemented.
var GetLocalFeaturedExtensions func(ctx context.Context, db dbutil.DB) ([]graphqlbackend.RegistryExtension, error)

// GetFeaturedExtensions returns the set of featured extensions.
//
// If this is sourcegraph.com, these are local extensions. Otherwise, these are remote extensions
// retrieved from sourcegraph.com.
func GetFeaturedExtensions(ctx context.Context, db dbutil.DB) ([]graphqlbackend.RegistryExtension, error) {
	if envvar.SourcegraphDotComMode() && GetLocalFeaturedExtensions != nil {
		return GetLocalFeaturedExtensions(ctx, db)
	}

	// Get remote featured extensions if the remote registry is sourcegraph.com.
	registryURL, err := getRemoteRegistryURL()
	if registryURL == nil || registryURL.String() != "https://sourcegraph.com/.api/registry" || err != nil {
		return nil, err
	}

	remote, err := registry.GetFeaturedExtensions(ctx, registryURL)
	if err != nil {
		return nil, err
	}
	remote = FilterRemoteExtensions(remote)
	for _, x := range remote {
		x.RegistryURL = registryURL.String()
	}
	registryExtensions := make([]graphqlbackend.RegistryExtension, len(remote))
	for i, x := range remote {
		registryExtensions[i] = &registryExtensionRemoteResolver{v: x}
	}

	return registryExtensions, nil
}

// IsWorkInProgressExtension reports whether the extension manifest indicates that this extension is
// marked as a work-in-progress extension (by having a "wip": true property).
func IsWorkInProgressExtension(manifest *string) bool {
	if manifest == nil {
		// Extensions with no manifest (== no releases published yet) are considered
		// work-in-progress.
		return true
	}

	// jsonc-parsing the manifest can be slow, so do a first pass. If the manifest doesn't even
	// contain `"wip"`, then there is no way that it could be WIP.
	if !strings.Contains(*manifest, `"wip"`) {
		return false
	}

	var result schema.SourcegraphExtensionManifest
	if err := jsonc.Unmarshal(*manifest, &result); err != nil {
		// An extension whose manifest fails to parse is problematic for other reasons (and an error
		// will be displayed), but it isn't helpful to also consider it work-in-progress.
		return false
	}

	return result.Wip
}
