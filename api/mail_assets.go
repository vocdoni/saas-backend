package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/notifications/mailtemplates"
)

// mailAssetHandler godoc
//
//	@Summary		Get an email image
//	@Description	Get one of the Vocdoni logo images the transactional emails link to. Only the images
//	@Description	embedded in the service are served; any other name is a 404. Cached for a day and
//	@Description	revalidated by ETag.
//	@Tags			mail
//	@Produce		image/png
//	@Param			file	path		string	true	"Image file name, e.g. vocdoni-wordmark-black-halo@2x.png"
//	@Success		200		{file}		binary	"The image"
//	@Success		304		{string}	string	"Not modified: If-None-Match matched the ETag"
//	@Failure		404		{string}	string	"No such image"
//	@Router			/mail/assets/{file} [get]
func mailAssetHandler(w http.ResponseWriter, r *http.Request) {
	mailtemplates.ServeAsset(w, r, chi.URLParam(r, "file"))
}
