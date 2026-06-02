package conversation

import (
	"context"
	"strings"

	domainconversation "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/conversation"
	models "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/persistence/models"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/repository"
	"gorm.io/gorm"
)

// CreateConversationProject 创建会话项目分组。
func (r *Repo) CreateConversationProject(ctx context.Context, item *domainconversation.ConversationProject) error {
	entity := toConversationProjectModel(item)
	if err := r.db.WithContext(ctx).Create(&entity).Error; err != nil {
		return translateError(err)
	}
	*item = toConversationProjectDomain(entity)
	return nil
}

// ListConversationProjects 查询用户项目分组。
func (r *Repo) ListConversationProjects(ctx context.Context, userID uint, statusFilter string) ([]domainconversation.ConversationProject, error) {
	items := make([]models.ConversationProject, 0)
	query := r.db.WithContext(ctx).
		Where("user_id = ?", userID)
	switch strings.TrimSpace(statusFilter) {
	case "archived":
		query = query.Where("status = ?", "archived")
	case "all":
		// 保留全部状态。
	default:
		query = query.Where("status = ?", "active")
	}
	if err := query.
		Order("sort_order ASC").
		Order("id DESC").
		Find(&items).Error; err != nil {
		return nil, translateError(err)
	}
	return toConversationProjectDomains(items), nil
}

// GetConversationProjectByPublicID 查询用户项目分组。
func (r *Repo) GetConversationProjectByPublicID(ctx context.Context, userID uint, publicID string) (*domainconversation.ConversationProject, error) {
	var item models.ConversationProject
	if err := r.db.WithContext(ctx).
		Where("user_id = ? AND public_id = ?", userID, strings.TrimSpace(publicID)).
		First(&item).Error; err != nil {
		return nil, translateError(err)
	}
	result := toConversationProjectDomain(item)
	return &result, nil
}

// UpdateConversationProjectMetadataByPublicID 更新项目分组元信息。
func (r *Repo) UpdateConversationProjectMetadataByPublicID(
	ctx context.Context,
	userID uint,
	publicID string,
	patch domainconversation.ConversationProjectPatch,
) (*domainconversation.ConversationProject, error) {
	updates := make(map[string]interface{})
	if patch.Name != nil {
		updates["name"] = *patch.Name
	}
	if patch.Description != nil {
		updates["description"] = *patch.Description
	}
	if patch.SystemPrompt != nil {
		updates["system_prompt"] = *patch.SystemPrompt
	}
	if patch.Color != nil {
		updates["color"] = *patch.Color
	}
	if patch.Icon != nil {
		updates["icon"] = *patch.Icon
	}
	if patch.Status != nil {
		updates["status"] = *patch.Status
	}
	if len(updates) == 0 {
		return r.GetConversationProjectByPublicID(ctx, userID, publicID)
	}
	result := r.db.WithContext(ctx).
		Model(&models.ConversationProject{}).
		Where("user_id = ? AND public_id = ?", userID, strings.TrimSpace(publicID)).
		Updates(updates)
	if result.Error != nil {
		return nil, translateError(result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, repository.ErrNotFound
	}
	return r.GetConversationProjectByPublicID(ctx, userID, publicID)
}

// DeleteConversationProjectByPublicID 删除项目分组，可选择一并软删除其下会话并返回可清理文件 ID。
func (r *Repo) DeleteConversationProjectByPublicID(
	ctx context.Context,
	userID uint,
	publicID string,
	deleteConversations bool,
	deleteFiles bool,
) ([]string, error) {
	normalizedPublicID := strings.TrimSpace(publicID)
	cleanupFileIDs := make([]string, 0)
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var project models.ConversationProject
		if err := tx.Where("user_id = ? AND public_id = ?", userID, normalizedPublicID).First(&project).Error; err != nil {
			return translateError(err)
		}
		// 项目删除与会话归属处理必须保持原子性，避免项目删除后留下不可见的项目引用。
		if deleteConversations {
			conversationIDs := make([]uint, 0)
			if deleteFiles {
				if err := tx.Model(&models.Conversation{}).
					Where("user_id = ? AND project_id = ?", userID, project.ID).
					Pluck("id", &conversationIDs).Error; err != nil {
					return translateError(err)
				}
			}
			if err := tx.
				Where("user_id = ? AND project_id = ?", userID, project.ID).
				Delete(&models.Conversation{}).Error; err != nil {
				return translateError(err)
			}
			if deleteFiles {
				fileIDs, err := listConversationFileCleanupCandidates(tx, userID, conversationIDs)
				if err != nil {
					return err
				}
				cleanupFileIDs = fileIDs
			}
		} else {
			if err := tx.Model(&models.Conversation{}).
				Where("user_id = ? AND project_id = ?", userID, project.ID).
				Update("project_id", nil).Error; err != nil {
				return translateError(err)
			}
		}
		if err := tx.Delete(&project).Error; err != nil {
			return translateError(err)
		}
		return nil
	})
	if err != nil {
		return nil, translateError(err)
	}
	return cleanupFileIDs, nil
}

// ReorderConversationProjects 更新项目展示顺序。
func (r *Repo) ReorderConversationProjects(ctx context.Context, userID uint, publicIDs []string) error {
	if len(publicIDs) == 0 {
		return nil
	}
	return translateError(r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for index, publicID := range publicIDs {
			result := tx.Model(&models.ConversationProject{}).
				Where("user_id = ? AND public_id = ?", userID, strings.TrimSpace(publicID)).
				Update("sort_order", index+1)
			if result.Error != nil {
				return translateError(result.Error)
			}
			if result.RowsAffected == 0 {
				return repository.ErrNotFound
			}
		}
		return nil
	}))
}

// UpdateConversationProjectAssignmentByPublicID 更新单个会话的项目归属。
func (r *Repo) UpdateConversationProjectAssignmentByPublicID(
	ctx context.Context,
	userID uint,
	conversationPublicID string,
	projectID *uint,
) (*domainconversation.Conversation, error) {
	result := r.db.WithContext(ctx).
		Model(&models.Conversation{}).
		Where("user_id = ? AND public_id = ?", userID, strings.TrimSpace(conversationPublicID)).
		Update("project_id", projectID)
	if result.Error != nil {
		return nil, translateError(result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, repository.ErrNotFound
	}
	return r.GetConversationByPublicID(ctx, conversationPublicID, userID)
}

// BatchUpdateConversationProjectByPublicIDs 批量更新会话项目归属。
func (r *Repo) BatchUpdateConversationProjectByPublicIDs(
	ctx context.Context,
	userID uint,
	conversationPublicIDs []string,
	projectID *uint,
) (int64, error) {
	if len(conversationPublicIDs) == 0 {
		return 0, nil
	}
	result := r.db.WithContext(ctx).
		Model(&models.Conversation{}).
		Where("user_id = ? AND public_id IN ?", userID, conversationPublicIDs).
		Update("project_id", projectID)
	if result.Error != nil {
		return 0, translateError(result.Error)
	}
	return result.RowsAffected, nil
}

func toConversationProjectDomain(item models.ConversationProject) domainconversation.ConversationProject {
	return domainconversation.ConversationProject{
		ID:           item.ID,
		UserID:       item.UserID,
		PublicID:     item.PublicID,
		Name:         item.Name,
		Description:  item.Description,
		SystemPrompt: item.SystemPrompt,
		Color:        item.Color,
		Icon:         item.Icon,
		SortOrder:    item.SortOrder,
		Status:       item.Status,
		CreatedAt:    item.CreatedAt,
		UpdatedAt:    item.UpdatedAt,
	}
}

func toConversationProjectDomains(items []models.ConversationProject) []domainconversation.ConversationProject {
	results := make([]domainconversation.ConversationProject, 0, len(items))
	for _, item := range items {
		results = append(results, toConversationProjectDomain(item))
	}
	return results
}

func toConversationProjectModel(item *domainconversation.ConversationProject) models.ConversationProject {
	if item == nil {
		return models.ConversationProject{}
	}
	return models.ConversationProject{
		UserID:       item.UserID,
		PublicID:     item.PublicID,
		Name:         item.Name,
		Description:  item.Description,
		SystemPrompt: item.SystemPrompt,
		Color:        item.Color,
		Icon:         item.Icon,
		SortOrder:    item.SortOrder,
		Status:       item.Status,
	}
}
