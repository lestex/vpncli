package bootstrap

import (
	"context"
	"fmt"
	"strings"
)

// XrayVersion is the Xray-core release installed on every server.
//
// It is pinned rather than resolved to "latest" so that what a server gets is
// what was tested, and so that two servers provisioned a week apart are the
// same server. Upgrading it is a deliberate edit: bump the tag, replace the
// checksums below with the ones from the release's .dgst files, and try it.
const XrayVersion = "v26.3.27"

// xrayChecksums is the SHA256 of each release asset, keyed by the asset name
// as it appears in the download URL.
//
// Verifying is the whole point of pinning. Without it the install is whatever
// the download happened to return, from a host that has been compromised
// before and will be again.
var xrayChecksums = map[string]string{
	"64":        "23cd9af937744d97776ee35ecad4972cf4b2109d1e0fe6be9930467608f7c8ae",
	"arm64-v8a": "4d30283ae614e3057f730f67cd088a42be6fdf91f8639d82cb69e48cde80413c",
}

// assets maps what `uname -m` reports to the release asset for it. Only the
// architectures our providers actually offer are here: an unknown one is an
// error rather than a guess that installs a binary the machine cannot run.
var assets = map[string]string{
	"x86_64":  "64",
	"amd64":   "64",
	"aarch64": "arm64-v8a",
	"arm64":   "arm64-v8a",
}

// release asks the server what it is and returns the asset and checksum for it.
func release(ctx context.Context, c Runner) (asset, checksum string, err error) {
	out, err := c.Run(ctx, "uname -m")
	if err != nil {
		return "", "", err
	}

	machine := strings.TrimSpace(out)
	asset, ok := assets[machine]
	if !ok {
		return "", "", fmt.Errorf("no Xray-core build for %s", machine)
	}

	checksum, ok = xrayChecksums[asset]
	if !ok {
		return "", "", fmt.Errorf("no checksum recorded for the %s build", asset)
	}
	return asset, checksum, nil
}

// downloadURL is where a release asset lives.
func downloadURL(asset string) string {
	return fmt.Sprintf("https://github.com/XTLS/Xray-core/releases/download/%s/Xray-linux-%s.zip",
		XrayVersion, asset)
}
