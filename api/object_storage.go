package api

import (
	"fmt"
	"net/http"
	"regexp"

	"github.com/go-chi/chi/v5"
)

// isObjectNameRgx is a regular expression to match object names.
var isObjectNameRgx = regexp.MustCompile(`^([a-zA-Z0-9]+)\.(jpg|jpeg|png)`)

// uploadImageWithFormHandler handles the uploading of images through a multipart form.
// It expects the request to contain a "file" field with one or more files to be uploaded.
func (a *API) uploadImageWithFormHandler(w http.ResponseWriter, r *http.Request) {
	// check if the user is authenticated
	// get the user from the request context
	user, ok := userFromContext(r.Context())
	if !ok {
		ErrUnauthorized.Write(w)
		return
	}

	// 32 MB is the default used by FormFile() function
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		ErrStorageInvalidObject.Withf("could not parse form: %v", err).Write(w)
		return
	}

	// Get a reference to the fileHeaders.
	// They are accessible only after ParseMultipartForm is called
	// files := r.MultipartForm.File["file"]
	var returnURLs []string
	for _, fileHeaders := range r.MultipartForm.File {
		for _, fileHeader := range fileHeaders {
			// Open the file
			file, err := fileHeader.Open()
			if err != nil {
				ErrStorageInvalidObject.Withf("cannot open file %v", err).Write(w)
				break
			}
			defer func() {
				if err := file.Close(); err != nil {
					ErrStorageInvalidObject.Withf("cannot close file %v", err).Write(w)
					return
				}
			}()
			// upload the file using the object storage client
			// and get the URL of the uploaded file
			storedFileID, err := a.objectStorage.Put(file, fileHeader.Size, user.Email)
			if err != nil {
				ErrInternalStorageError.With(err.Error()).Write(w)
				break
			}
			returnURLs = append(returnURLs, objectURL(a.serverURL, storedFileID))
		}
	}
	httpWriteJSON(w, map[string][]string{"urls": returnURLs})
}

// downloadImageInlineHandler handles the HTTP request to download an image inline.
// It retrieves the object ID from the URL parameters, fetches the object from the
// object storage, and writes the object data to the HTTP response with appropriate
// headers for inline display.
func (a *API) downloadImageInlineHandler(w http.ResponseWriter, r *http.Request) {
	objectName := chi.URLParam(r, "objectName")
	if objectName == "" {
		ErrMalformedURLParam.With("objectName is required").Write(w)
		return
	}
	objectID, ok := objectIDfromName(objectName)
	if !ok {
		ErrStorageInvalidObject.With("invalid objectName").Write(w)
		return
	}
	// get the object from the object storage client
	object, err := a.objectStorage.Get(objectID)
	if err != nil {
		ErrStorageInvalidObject.Withf("cannot get object %v", err).Write(w)
		return
	}
	// write the object to the response
	w.Header().Set("Content-Type", object.ContentType)
	// w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.Header().Set("Content-Disposition", "inline")
	if _, err := w.Write(object.Data); err != nil {
		ErrInternalStorageError.Withf("cannot write object %v", err).Write(w)
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
