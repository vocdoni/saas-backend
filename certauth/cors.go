package certauth

import (
	"net/http"
	"regexp"

	"github.com/go-chi/cors"
)

// jsonEndpointRe matches the paths of the two main-listener certificate endpoints.
var jsonEndpointRe = regexp.MustCompile(`^/processes/[0-9a-fA-F]{24}/auth/cert(/redeem)?$`)

// CORS returns the main router's CORS middleware with the feature on: the two certificate JSON
// endpoints answer preflights and requests for the allowlisted app origins only, without
// credentials (contract section 13); every other path goes through general, the router's CORS
// as it is with the feature off. chi cannot swap a middleware per route, so the one CORS slot
// dispatches by path.
func (s *Service) CORS(general func(http.Handler) http.Handler) func(http.Handler) http.Handler {
	cert := cors.New(cors.Options{
		AllowedOrigins:   s.AllowedOrigins(),
		AllowedMethods:   []string{http.MethodPost, http.MethodOptions},
		AllowedHeaders:   []string{"Accept", "Content-Type"},
		AllowCredentials: false,
		MaxAge:           300,
	}).Handler
	return func(next http.Handler) http.Handler {
		generalNext, certNext := general(next), cert(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if jsonEndpointRe.MatchString(r.URL.Path) {
				certNext.ServeHTTP(w, r)
				return
			}
			generalNext.ServeHTTP(w, r)
		})
	}
}
