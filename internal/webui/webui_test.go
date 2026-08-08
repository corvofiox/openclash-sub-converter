// webui 静态资源安全头测试（P3-16）。
package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestSecurityHeaders 断言静态资源响应带安全头：CSP（default-src 'self'）、
// X-Frame-Options: DENY、Cache-Control: no-cache。
func TestSecurityHeaders(t *testing.T) {
	h := Handler()
	for _, path := range []string{"/", "/app.js", "/style.css"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status = %d, want 200", path, rec.Code)
			continue
		}
		csp := rec.Header().Get("Content-Security-Policy")
		if !strings.Contains(csp, "default-src 'self'") {
			t.Errorf("%s: CSP = %q, want 含 default-src 'self'", path, csp)
		}
		if xf := rec.Header().Get("X-Frame-Options"); xf != "DENY" {
			t.Errorf("%s: X-Frame-Options = %q, want DENY", path, xf)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-cache" {
			t.Errorf("%s: Cache-Control = %q, want no-cache", path, cc)
		}
	}
}
