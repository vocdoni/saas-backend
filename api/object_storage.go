package api

import (
	"net/http"

	"github.com/go-chi/chi/v5"
)

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
		ErrGenericInternalServerError.With("could not parse form").Write(w)
		return
	}

	// Get a reference to the fileHeaders.
	// They are accessible only after ParseMultipartForm is called
	files := r.MultipartForm.File["file"]
	var returnURLs []string
	for _, fileHeader := range files {
		// Open the file
		file, err := fileHeader.Open()
		if err != nil {
			ErrGenericInternalServerError.Withf("cannot open file %s", err.Error()).Write(w)
			break
		}
		defer func() {
			if err := file.Close(); err != nil {
				ErrGenericInternalServerError.Withf("cannot close file %s", err.Error()).Write(w)
				return
			}
		}()
		// upload the file using the object storage client
		// and get the URL of the uploaded file
		url, err := a.objectStorage.Put(file, fileHeader.Size, user.Email)
		if err != nil {
			ErrGenericInternalServerError.Withf("cannot upload file %s", err.Error()).Write(w)
			break
		}
		returnURLs = append(returnURLs, url)
	}
	httpWriteJSON(w, map[string][]string{"urls": returnURLs})
}

func (a *API) downloadImageInlineHandler(w http.ResponseWriter, r *http.Request) {
	objectID := chi.URLParam(r, "objectName")
	if objectID == "" {
		ErrMalformedURLParam.With("objectID is required").Write(w)
		return
	}
	// get the object from the object storage client
	object, err := a.objectStorage.Get(objectID)
	if err != nil {
		ErrGenericInternalServerError.Withf("cannot get object %s", err.Error()).Write(w)
		return
	}
	// write the object to the response
	w.Header().Set("Content-Type", object.ContentType)
	// w.Header().Set("Content-Length", fmt.Sprintf("%d", len(data)))
	w.Header().Set("Content-Disposition", "inline")
	if _, err := w.Write(object.Data); err != nil {
		ErrGenericInternalServerError.Withf("cannot write object %s", err.Error()).Write(w)
		return
	}
}
