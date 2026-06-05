package announcement

import "errors"

var (
	// ErrInvalidAnnouncement 表示公告配置非法。
	ErrInvalidAnnouncement = errors.New("invalid announcement")
	// ErrAnnouncementNotFound 表示公告不存在。
	ErrAnnouncementNotFound = errors.New("announcement not found")
)
