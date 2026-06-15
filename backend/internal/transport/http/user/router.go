package user

import "github.com/gin-gonic/gin"

// RegisterPublicRoutes 注册用户域公开路由。
func (m *Module) RegisterPublicRoutes(public *gin.RouterGroup) {
	public.GET("/users/:public_id/avatar", m.Handler.GetAvatar)
}

// RegisterRoutes 用户域当前无独立登录态路由。
func (m *Module) RegisterRoutes(_ *gin.RouterGroup) {}
