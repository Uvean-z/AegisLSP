package sandbox

import "strings"

// containerPath converts a host path to a Docker-compatible container path.
// On Windows, converts C:\Users\foo to /c/Users/foo.
// On Unix, returns the path as-is.
func containerPath(hostPath string) string {
	if len(hostPath) >= 2 && hostPath[1] == ':' {
		drive := strings.ToLower(string(hostPath[0]))
		rest := strings.ReplaceAll(hostPath[2:], "\\", "/")
		return "/" + drive + rest
	}
	return hostPath
}
