package announcement

import "time"

const (
	// StatusActive 表示公告启用。
	StatusActive = "active"
	// StatusInactive 表示公告停用。
	StatusInactive = "inactive"

	// TypeCritical 表示紧急公告。
	TypeCritical = "critical"
	// TypeWarning 表示警告公告。
	TypeWarning = "warning"
	// TypeInfo 表示提示公告。
	TypeInfo = "info"
	// TypeNormal 表示普通公告。
	TypeNormal = "normal"
	// TypeGeneral 表示常规公告。
	TypeGeneral = "general"
)

// Announcement 表示一条站点公告。
type Announcement struct {
	ID              uint
	Title           string
	ContentMarkdown string
	Status          string
	Type            string
	Pinned          bool
	Priority        int
	StartsAt        *time.Time
	ExpiresAt       *time.Time
	CreatedByUserID uint
	CreatedAt       time.Time
	UpdatedAt       time.Time
	ClosedAt        *time.Time
}

// UserState 记录用户对公告版本的展示偏好。
type UserState struct {
	ID                    uint
	AnnouncementID        uint
	UserID                uint
	AnnouncementUpdatedAt time.Time
	DismissedUntil        *time.Time
	ClosedAt              *time.Time
	CreatedAt             time.Time
	UpdatedAt             time.Time
}
