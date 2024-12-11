package api

import (
	"io"
	"net/http"
)

func (a *API) uploadObjectStorageHandler(w http.ResponseWriter, r *http.Request) {
	// 32 MB is the default used by FormFile() function
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		ErrGenericInternalServerError.With("could not parse form").Write(w)
		return
	}

	// Get a reference to the fileHeaders.
	// They are accessible only after ParseMultipartForm is called
	files := r.MultipartForm.File["file"]
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
		_, err = file.Seek(0, io.SeekStart)
		if err != nil {
			ErrGenericInternalServerError.Withf("%s", err.Error()).Write(w)
			break
		}
		fileBytes, err := io.ReadAll(file)
		if err != nil {
			ErrGenericInternalServerError.Withf("cannot read file %s", err.Error()).Write(w)
			break
		}
		// upload the file using the object storage client
		// and get the URL of the uploaded file
		url, err := a.objectStorage.Upload(fileHeader.Filename, fileBytes)
		if err != nil {
			ErrGenericInternalServerError.Withf("cannot upload file %s", err.Error()).Write(w)
			break
		}
		httpWriteJSON(w, map[string]string{"url": url})
	}

}
