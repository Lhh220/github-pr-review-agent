package adminui

import (
	_ "embed"
	"net/http"

	"github.com/gin-gonic/gin"
)

//go:embed static/index.html
var indexHTML []byte

func Register(r *gin.Engine) {
	r.GET("/admin", handleIndex)
	r.GET("/admin/", handleIndex)
}

func handleIndex(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/html; charset=utf-8", indexHTML)
}
