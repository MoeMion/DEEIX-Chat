package user

import "strings"

const fileAvatarPrefix = "file:"

// BuildFileAvatarURL 生成内部文件头像引用。
func BuildFileAvatarURL(fileID string) string {
	return fileAvatarPrefix + strings.TrimSpace(fileID)
}

// ParseFileAvatarURL 解析内部文件头像引用。
func ParseFileAvatarURL(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	if !strings.HasPrefix(value, fileAvatarPrefix) {
		return "", false
	}
	fileID := strings.TrimPrefix(value, fileAvatarPrefix)
	if fileID == "" || fileID != strings.TrimSpace(fileID) {
		return "", false
	}
	if !IsValidFileAvatarID(fileID) {
		return "", false
	}
	return fileID, true
}

// IsValidFileAvatarID 判断文件头像引用中的文件 ID 是否为受限安全格式。
func IsValidFileAvatarID(fileID string) bool {
	value := strings.TrimSpace(fileID)
	if value == "" || value != fileID || len(value) > 128 || !strings.HasPrefix(value, "file_") {
		return false
	}
	for _, item := range value {
		if item >= 'a' && item <= 'z' {
			continue
		}
		if item >= 'A' && item <= 'Z' {
			continue
		}
		if item >= '0' && item <= '9' {
			continue
		}
		if item == '_' || item == '-' {
			continue
		}
		return false
	}
	return true
}
