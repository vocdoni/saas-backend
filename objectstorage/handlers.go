package objectstorage

import (
	"fmt"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"
	"github.com/vocdoni/saas-backend/api/apicommon"
	"github.com/vocdoni/saas-backend/errors"
)

// isObjectNameRgx is a regular expression to match object names.
var isObjectNameRgx = regexp.MustCompile(`^([a-zA-Z0-9]+)\.(jpg|jpeg|png)`)

// UploadImageWithFormHandler godoc
//	@Summary		Upload images
//	@Description	Upload images through a multipart form. Expects the request to contain a "file" field with one or more
//	@Description	files to be uploaded.
//	@Tags			storage
//	@Accept			multipart/form-data
//	@Produce		json
//	@Security		BearerAuth
//	@Param			file	formData	file				true	"Image file(s) to upload"
//	@Success		200		{object}	map[string][]string	"URLs of uploaded images"
//	@Failure		400		{object}	errors.Error		"Invalid input data"
//	@Failure		401		{object}	errors.Error		"Unauthorized"
//	@Failure		500		{object}	errors.Error		"Internal server error"
//	@Router			/storage [post]
func (osc *ObjectStorageClient) UploadImageWithFormHandler(w http.ResponseWriter, r *http.Request) {
	// check if the user is authenticated
	// get the user from the request context
	user, ok := apicommon.UserFromContext(r.Context())
	if !ok {
		errors.ErrUnauthorized.Write(w)
		return
	}

	// 32 MB is the default used by FormFile() function
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		errors.ErrStorageInvalidObject.Withf("could not parse form: %v", err).Write(w)
		return
	}

	// Get a reference to the fileHeaders.
	// They are accessible only after ParseMultipartForm is called
	filesFound := false
	var returnURLs []string
	for _, fileHeaders := range r.MultipartForm.File {
		for _, fileHeader := range fileHeaders {
			// Open the file
			file, err := fileHeader.Open()
			if err != nil {
				errors.ErrStorageInvalidObject.Withf("cannot open file %s %v", fileHeader.Filename, err).Write(w)
				return
			}
			defer func() {
				if err := file.Close(); err != nil {
					errors.ErrStorageInvalidObject.Withf("cannot close file %s  %v", fileHeader.Filename, err).Write(w)
					return
				}
			}()
			// upload the file using the object storage client
			// and get the URL of the uploaded file
			filesFound = true
			storedFileID, err := osc.Put(file, fileHeader.Size, user.Email)
			if err != nil {
				errors.ErrInternalStorageError.Withf("%s %v", fileHeader.Filename, err).Write(w)
				return
			}
			returnURLs = append(returnURLs, objectURL(osc.ServerURL, storedFileID))
		}
	}
	if !filesFound {
		errors.ErrStorageInvalidObject.With("no files found").Write(w)
		return
	}
	apicommon.HttpWriteJSON(w, map[string][]string{"urls": returnURLs})
}

// DownloadImageInlineHandler godoc
//	@Summary		Download an image
//	@Description	Download an image inline. Retrieves the object from storage and displays it in the browser.
//	@Tags			storage
//	@Produce		image/jpeg,image/png
//	@Param			objectName	path		string			true	"Object name"
//	@Success		200			{file}		binary			"Image file"
//	@Failure		400			{object}	errors.Error	"Invalid object name"
//	@Failure		404			{object}	errors.Error	"Object not found"
//	@Failure		500			{object}	errors.Error	"Internal server error"
//	@Router			/storage/{objectName} [get]
func (osc *ObjectStorageClient) DownloadImageInlineHandler(w http.ResponseWriter, r *http.Request) {
	objectName := chi.URLParam(r, "objectName")
	if objectName == "" {
		errors.ErrMalformedURLParam.With("objectName is required").Write(w)
		return
	}
	objectID, ok := objectIDfromName(objectName)
	if !ok {
		errors.ErrStorageInvalidObject.With("invalid objectName").Write(w)
		return
	}
	// get the object from the object storage client
	object, err := osc.Get(objectID)
	if err != nil {
		errors.ErrStorageInvalidObject.Withf("cannot get object %v", err).Write(w)
		return
	}
	// write the object to the response
	w.Header().Set("Content-Type", object.ContentType)
	// w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.Header().Set("Content-Disposition", "inline")
	if _, err := w.Write(object.Data); err != nil {
		errors.ErrInternalStorageError.Withf("cannot write object %v", err).Write(w)
		return
	}
}

// objectURL returns the URL for the object with the given objectID.
func objectURL(baseURL, objectID string) string {
	return fmt.Sprintf("%s/storage/%s", baseURL, objectID)
}

// objectIDfromURL returns the objectID from the given URL. If the URL is not an
// object URL, it returns an empty string and false.
func objectIDfromName(url string) (string, bool) {
	objectID := isObjectNameRgx.FindStringSubmatch(url)
	if len(objectID) != 3 {
		return "", false
	}
	return objectID[1], true
}
