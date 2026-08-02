package server

import (
	"fmt"
	"sort"
	"strings"
)

// placementReq is a runner+image request at one precedence level (a
// submission override, a repo's config, or the resolved result).
type placementReq struct {
	runner string
	image  string
}

// resolvePlacement resolves where a run executes, applying the precedence
// submission > repo config > daemon default for the runner and — only when
// the resolved runner is "vm" — submission > repo config > the launcher's
// "default" for the image (per-repo VM config, VM3).
//
// A runner explicitly requested by the submission or the repo that names no
// registered launcher, and a vm image that names no registered image, are
// rejected with a name-listing error, giving the caller a clear submit
// rejection (spec §Dispatch resolution). The daemon default runner is
// trusted — it was validated at serve startup — so a server built without a
// full launcher registry still dispatches. When the resolved runner is not
// "vm" the image is dropped, since it is meaningless off the vm launcher.
func resolvePlacement(sub, repo placementReq, defaultRunner string, launchers map[string]Launcher) (placementReq, error) {
	runner := sub.runner
	explicit := runner != ""
	if runner == "" {
		runner = repo.runner
		explicit = runner != ""
	}
	if runner == "" {
		runner = defaultRunner
	}
	if explicit {
		if _, ok := launchers[runner]; !ok {
			return placementReq{}, fmt.Errorf("unknown runner %q (registered: %s)", runner, sortedLauncherNames(launchers))
		}
	}
	if runner != "vm" {
		return placementReq{runner: runner}, nil
	}
	image := sub.image
	if image == "" {
		image = repo.image
	}
	if image != "" {
		if v, ok := launchers["vm"].(ImageValidator); ok && !v.HasImage(image) {
			return placementReq{}, fmt.Errorf("unknown vm image %q (registered: %s)", image, strings.Join(v.ImageNames(), ", "))
		}
	}
	return placementReq{runner: runner, image: image}, nil
}

// sortedLauncherNames lists the registered launcher names, sorted, for a
// diagnosable unknown-runner rejection.
func sortedLauncherNames(launchers map[string]Launcher) string {
	names := make([]string, 0, len(launchers))
	for name := range launchers {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
