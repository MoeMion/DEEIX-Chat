package user

import (
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	appuser "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/application/user"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/shared/response"
	"github.com/gin-gonic/gin"
)

// Handler 预留用户域 HTTP 处理器（当前用户管理在 admin 模块暴露）。
type Handler struct {
	service *appuser.Service
}

// NewHandler 创建处理器。
func NewHandler(service *appuser.Service) *Handler {
	return &Handler{service: service}
}

// GetAvatar 获取用户当前上传头像内容。
func (h *Handler) GetAvatar(c *gin.Context) {
	publicID := strings.TrimSpace(c.Param("public_id"))
	if publicID == "" {
		response.Error(c, http.StatusBadRequest, "invalid user id")
		return
	}
	result, err := h.service.OpenAvatarContent(c.Request.Context(), publicID)
	if err != nil {
		switch {
		case errors.Is(err, appuser.ErrUserNotFound),
			errors.Is(err, appuser.ErrAvatarNotFound):
			response.Error(c, http.StatusNotFound, "avatar not found")
			return
		default:
			response.Error(c, http.StatusInternalServerError, "open avatar failed")
			return
		}
	}
	defer result.Reader.Close() //nolint:errcheck

	contentType := strings.TrimSpace(result.ContentType)
	if !strings.HasPrefix(strings.ToLower(contentType), "image/") {
		response.Error(c, http.StatusNotFound, "avatar not found")
		return
	}
	c.Header("Content-Type", contentType)
	c.Header("Content-Disposition", "inline")
	c.Header("Cache-Control", "public, max-age=300")
	c.Header("X-Content-Type-Options", "nosniff")
	if result.SizeBytes > 0 {
		c.Header("Content-Length", strconv.FormatInt(result.SizeBytes, 10))
	}
	if !result.ModTime.IsZero() {
		c.Header("Last-Modified", result.ModTime.UTC().Format(http.TimeFormat))
	}
	if _, err = io.Copy(c.Writer, result.Reader); err != nil {
		c.Abort()
		return
	}
}
