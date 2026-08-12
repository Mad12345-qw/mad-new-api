package router

import (
	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"

	"github.com/gin-gonic/gin"
)

func SetCPAControlRouter(router *gin.Engine) {
	control := router.Group("/internal/madapi/cpa")
	control.Use(middleware.RouteTag("cpa-control"))
	control.Use(middleware.BodyStorageCleanup())
	control.Use(middleware.CPAControlAuth())
	{
		control.GET("/auth", middleware.TokenAuth(), controller.CPAControlAuth)
		control.POST("/dispatch",
			middleware.TokenAuth(),
			middleware.CPAControlRequestPath(),
			middleware.CPAControlModelSlots(),
			middleware.Distribute(),
			controller.CPAControlDispatch,
		)
		control.POST("/settle", controller.CPAControlSettle)
	}
}
