package api

import (
	"net/http"
	"runtime/debug"

	"github.com/vocdoni/saas-backend/api/apicommon"
)

// VersionInfo contains build and version information for the service.
type VersionInfo struct {
	Version   string `json:"version"`
	GoVersion string `json:"goVersion"`
}

// defaultVersionInfo is returned when build info is unavailable.
var defaultVersionInfo = VersionInfo{
	Version:   "unknown",
	GoVersion: "unknown",
}

// VersionHandler godoc
//
//	@Summary		Get service version
//	@Description	Returns the service version and build information
//	@Tags			health
//	@Produce		json
//	@Success		200	{object}	VersionInfo
//	@Router			/version [get]
func (*API) VersionHandler(w http.ResponseWriter, _ *http.Request) {
	v, _ := debug.ReadBuildInfo()
	if v == nil {
		apicommon.HTTPWriteJSON(w, defaultVersionInfo)
		return
	}
	apicommon.HTTPWriteJSON(w, VersionInfo{
		Version:   v.Main.Version,
		GoVersion: v.GoVersion,
	})
}
