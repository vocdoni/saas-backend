package api

import (
	"fmt"
	"io"
	"net/http"
	"strings"

	"golang.org/x/crypto/sha3"
)

func (a *API) uploadObjectStorageWithOriginHandler(w http.ResponseWriter, r *http.Request) {
	// check if the user is authenticated
	// get the user from the request context
	user, ok := userFromContext(r.Context())
	if !ok {
		ErrUnauthorized.Write(w)
		return
	}
	meta := map[string]string{"address": user.Email}
	// get the file origin ("organization" or "election") from the request
	origin := r.URL.Query().Get("origin")
	if origin == "" {
		origin = "organization"
	} else if origin != "organization" && origin != "election" {
		ErrMalformedURLParam.With("origin must be 'organization' or 'election'").Write(w)
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
		buff := make([]byte, 512)
		_, err = file.Read(buff)
		if err != nil {
			ErrGenericInternalServerError.Withf("cannot read file %s", err.Error()).Write(w)
			break
		}
		// checking the content type
		// so we don't allow files other than images
		filetype := http.DetectContentType(buff)
		if filetype != "image/jpeg" && filetype != "image/png" && filetype != "image/jpg" {
			ErrGenericInternalServerError.With("The provided file format is not allowed. Please upload a JPEG,JPG or PNG image").Write(w)
			break
		}
		// get the file extension
		fileExtension := strings.Split(filetype, "/")[1]
		_, err = file.Seek(0, io.SeekStart)
		if err != nil {
			ErrGenericInternalServerError.Withf("%s", err.Error()).Write(w)
			break
		}
		// Calculate filename using
		// the origin, the SHA3-256 hash of the file and the file extension
		// ${origin}/${sha3-256(file)}.${fileExtension}
		hash := fmt.Sprintf("%x", sha3.Sum224(buff))
		filename := fmt.Sprintf("%s/%s.%s", origin, hash, fileExtension)
		// upload the file using the object storage client
		// and get the URL of the uploaded file
		if _, err = a.objectStorage.Put(filename, "inline", filetype, fileHeader.Size, file, meta); err != nil {
			ErrGenericInternalServerError.Withf("cannot upload file %s", err.Error()).Write(w)
			break
		}
		returnURLs = append(returnURLs, filename)
	}
	httpWriteJSON(w, map[string][]string{"urls": returnURLs})
}
