package controller

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/database/model"
)

func TestNativePolicyRoutingRequiresAdminScope(t *testing.T) {
	for _, scope := range []string{model.ApiScopeAdmin, model.ApiScopeMonitor, model.ApiScopeNodeSync, model.ApiScopeNodeAdmin} {
		for _, endpoint := range []struct{ method, path string }{{http.MethodGet, "/policy-routing"}, {http.MethodPost, "/policy-routing/preview"}, {http.MethodPost, "/policy-routing/apply"}} {
			t.Run(scope+endpoint.path, func(t *testing.T) {
				r := gin.New()
				g := r.Group("/panel/api")
				g.Use(func(c *gin.Context) { c.Set("api_token_scope", scope); c.Next() }, (&APIController{}).enforceTokenScope)
				g.Handle(endpoint.method, endpoint.path, func(c *gin.Context) { c.Status(http.StatusNoContent) })
				w := httptest.NewRecorder()
				r.ServeHTTP(w, httptest.NewRequest(endpoint.method, "/panel/api"+endpoint.path, strings.NewReader(`{}`)))
				want := http.StatusForbidden
				if scope == model.ApiScopeAdmin {
					want = http.StatusNoContent
				}
				if w.Code != want {
					t.Fatalf("scope %s: got %d, want %d", scope, w.Code, want)
				}
			})
		}
	}
}
