package controller

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/mhsanaei/3x-ui/v3/internal/policyrouting"
	"github.com/mhsanaei/3x-ui/v3/internal/web/service"
)

func NewPolicyRoutingController(g *gin.RouterGroup) {
	g.GET("", policyRoutingView)
	g.POST("/preview", policyRoutingPreview)
	g.POST("/apply", policyRoutingApply)
}

// policyRoutingView godoc
// @Summary Read container policy routing configuration and kernel state
// @Tags Native policy routing
// @Produce json
// @Success 200 {object} entity.Msg{obj=service.PolicyRoutingView}
// @Router /panel/api/policy-routing [get]
// @Security BearerAuth
func policyRoutingView(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 10*time.Second)
	defer cancel()
	v, err := (&service.PolicyRoutingService{}).View(ctx)
	jsonObj(c, v, err)
}

func bindPolicy(c *gin.Context) (policyrouting.Config, error) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 128<<10)
	var cfg policyrouting.Config
	if err := c.ShouldBindJSON(&cfg); err != nil {
		return cfg, err
	}
	return cfg, cfg.Validate()
}

// policyRoutingPreview godoc
// @Summary Validate and preview container packet-marking rules without applying
// @Tags Native policy routing
// @Accept json
// @Produce json
// @Param config body policyrouting.Config true "Desired IPv4 policy"
// @Success 200 {object} entity.Msg{obj=string}
// @Router /panel/api/policy-routing/preview [post]
// @Security BearerAuth
func policyRoutingPreview(c *gin.Context) {
	cfg, err := bindPolicy(c)
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	jsonObj(c, cfg.NFTScript(true), nil)
}

// policyRoutingApply godoc
// @Summary Apply and save container policy routing, rolling back on failure
// @Tags Native policy routing
// @Accept json
// @Produce json
// @Param config body policyrouting.Config true "Desired IPv4 policy"
// @Success 200 {object} entity.Msg
// @Router /panel/api/policy-routing/apply [post]
// @Security BearerAuth
func policyRoutingApply(c *gin.Context) {
	cfg, err := bindPolicy(c)
	if err != nil {
		jsonObj(c, nil, err)
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), 15*time.Second)
	defer cancel()
	err = (&service.PolicyRoutingService{}).Apply(ctx, cfg)
	jsonObj(c, nil, err)
}
