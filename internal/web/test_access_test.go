package web

import "net/http"

// Existing routing/filesystem tests run as an authenticated local admin.
// Boundary/security tests use the production handlers directly instead.
func testAdminHandler(s *Server) http.Handler {
	if len(s.cfg.Users) == 0 && s.cfg.AuthPassword == "" {
		s.cfg.AuthPassword = "test-admin-secret"
	}
	handler := s.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.RemoteAddr = "127.0.0.1:12345"
		r.Host = "127.0.0.1"
		if _, _, ok := r.BasicAuth(); !ok {
			r.SetBasicAuth(s.cfg.AuthUsername, s.cfg.AuthPassword)
		}
		handler.ServeHTTP(w, r)
	})
}
func testLANRequest(r *http.Request) {
	r.RemoteAddr = "127.0.0.1:12345"
	r.Host = "127.0.0.1"
}
