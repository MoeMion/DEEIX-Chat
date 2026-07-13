package mcp

import "github.com/gin-gonic/gin"

func (m *Module) RegisterRoutes(authGroup *gin.RouterGroup) {
	group := authGroup.Group("/mcp")
	group.GET("/tools", m.Handler.ListAvailableTools)
}

func (m *Module) RegisterAdminRoutes(adminGroup *gin.RouterGroup) {
	group := adminGroup.Group("/mcp")
	group.GET("/servers", m.Handler.ListServers)
	group.POST("/servers", m.Handler.CreateServer)
	group.POST("/header-templates/preview", m.Handler.PreviewHeaderTemplate)
	group.PATCH("/servers/order", m.Handler.ReorderServers)
	group.PATCH("/servers/:id", m.Handler.UpdateServer)
	group.PATCH("/servers/:id/context-jwt", m.Handler.UpdateContextJWT)
	group.POST("/servers/:id/context-jwt/rotations", m.Handler.PrepareContextJWTRotation)
	group.POST("/servers/:id/context-jwt/rotations/:kid/activate", m.Handler.ActivateContextJWTRotation)
	group.DELETE("/servers/:id/context-jwt/rotations/:kid", m.Handler.CancelContextJWTRotation)
	group.DELETE("/servers/:id/context-jwt", m.Handler.DisableContextJWT)
	group.DELETE("/servers/:id", m.Handler.DeleteServer)
	group.POST("/servers/:id/probe", m.Handler.ProbeServer)
	group.GET("/servers/:id/tools", m.Handler.ListServerTools)
	group.PATCH("/servers/:id/tools/status", m.Handler.UpdateServerToolsStatus)
	group.POST("/servers/:id/sync", m.Handler.SyncServerTools)
	group.PATCH("/tools/:id", m.Handler.UpdateTool)
}
