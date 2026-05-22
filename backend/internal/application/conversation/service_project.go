package conversation

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	model "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/conversation"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/repository"
	"github.com/google/uuid"
)

const (
	conversationProjectNameMaxChars        = 80
	conversationProjectDescriptionMaxChars = 255
	conversationProjectMetaMaxChars        = 32
)

// ConversationProjectInput 定义新建项目分组输入。
type ConversationProjectInput struct {
	Name        string
	Description string
	Color       string
	Icon        string
}

// ConversationProjectPatchInput 定义项目分组局部更新输入。
type ConversationProjectPatchInput struct {
	Name        *string
	Description *string
	Color       *string
	Icon        *string
	Status      *string
}

// CreateConversationProject 创建当前用户的会话项目分组。
func (s *Service) CreateConversationProject(ctx context.Context, userID uint, input ConversationProjectInput) (*model.ConversationProject, error) {
	normalized, err := normalizeConversationProjectInput(input)
	if err != nil {
		return nil, err
	}
	item := &model.ConversationProject{
		UserID:      userID,
		PublicID:    normalizePublicID(uuid.NewString()),
		Name:        normalized.Name,
		Description: normalized.Description,
		Color:       normalized.Color,
		Icon:        normalized.Icon,
		Status:      "active",
	}
	if err = s.repo.CreateConversationProject(ctx, item); err != nil {
		return nil, err
	}
	return item, nil
}

// ListConversationProjects 查询当前用户项目分组。
func (s *Service) ListConversationProjects(ctx context.Context, userID uint, statusFilter string) ([]model.ConversationProject, error) {
	return s.repo.ListConversationProjects(ctx, userID, normalizeConversationProjectStatusFilter(statusFilter))
}

// UpdateConversationProject 更新当前用户项目分组。
func (s *Service) UpdateConversationProject(
	ctx context.Context,
	userID uint,
	publicID string,
	input ConversationProjectPatchInput,
) (*model.ConversationProject, error) {
	patch, err := normalizeConversationProjectPatch(input)
	if err != nil {
		return nil, err
	}
	item, err := s.repo.UpdateConversationProjectMetadataByPublicID(ctx, userID, strings.TrimSpace(publicID), patch)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrConversationProjectNotFound
		}
		return nil, err
	}
	return item, nil
}

// DeleteConversationProject 删除当前用户项目分组。
func (s *Service) DeleteConversationProject(ctx context.Context, userID uint, publicID string, deleteConversations bool) error {
	if err := s.repo.DeleteConversationProjectByPublicID(ctx, userID, strings.TrimSpace(publicID), deleteConversations); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return ErrConversationProjectNotFound
		}
		return err
	}
	return nil
}

// ReorderConversationProjects 更新当前用户项目展示顺序。
func (s *Service) ReorderConversationProjects(ctx context.Context, userID uint, publicIDs []string) error {
	normalizedIDs := normalizeProjectPublicIDs(publicIDs)
	if len(normalizedIDs) == 0 || len(normalizedIDs) != len(publicIDs) {
		return ErrInvalidConversationProject
	}
	if err := s.repo.ReorderConversationProjects(ctx, userID, normalizedIDs); err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return ErrConversationProjectNotFound
		}
		return err
	}
	return nil
}

// SetConversationProject 设置当前用户单个会话的项目归属，空项目 ID 表示解除归属。
func (s *Service) SetConversationProject(
	ctx context.Context,
	userID uint,
	conversationPublicID string,
	projectPublicID string,
) (*model.Conversation, error) {
	projectID, err := s.resolveConversationProjectID(ctx, userID, projectPublicID)
	if err != nil {
		return nil, err
	}
	item, err := s.repo.UpdateConversationProjectAssignmentByPublicID(ctx, userID, strings.TrimSpace(conversationPublicID), projectID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrConversationNotFound
		}
		return nil, err
	}
	return item, nil
}

// BatchSetConversationProject 批量设置当前用户会话项目归属。
func (s *Service) BatchSetConversationProject(
	ctx context.Context,
	userID uint,
	conversationPublicIDs []string,
	projectPublicID string,
) (int64, error) {
	normalizedConversationIDs := normalizeProjectPublicIDs(conversationPublicIDs)
	if len(normalizedConversationIDs) == 0 || len(normalizedConversationIDs) != len(conversationPublicIDs) {
		return 0, ErrInvalidConversationProject
	}
	projectID, err := s.resolveConversationProjectID(ctx, userID, projectPublicID)
	if err != nil {
		return 0, err
	}
	updated, err := s.repo.BatchUpdateConversationProjectByPublicIDs(ctx, userID, normalizedConversationIDs, projectID)
	if err != nil {
		return 0, err
	}
	if updated != int64(len(normalizedConversationIDs)) {
		return updated, ErrConversationNotFound
	}
	return updated, nil
}

func (s *Service) resolveConversationProjectID(ctx context.Context, userID uint, publicID string) (*uint, error) {
	normalizedPublicID := strings.TrimSpace(publicID)
	if normalizedPublicID == "" || normalizedPublicID == "unassigned" {
		return nil, nil
	}
	project, err := s.repo.GetConversationProjectByPublicID(ctx, userID, normalizedPublicID)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return nil, ErrConversationProjectNotFound
		}
		return nil, err
	}
	return &project.ID, nil
}

func normalizeConversationProjectInput(input ConversationProjectInput) (ConversationProjectInput, error) {
	normalized := ConversationProjectInput{
		Name:        strings.TrimSpace(input.Name),
		Description: strings.TrimSpace(input.Description),
		Color:       strings.TrimSpace(input.Color),
		Icon:        strings.TrimSpace(input.Icon),
	}
	if normalized.Name == "" || exceedsRuneLimit(normalized.Name, conversationProjectNameMaxChars) {
		return ConversationProjectInput{}, ErrInvalidConversationProject
	}
	if exceedsRuneLimit(normalized.Description, conversationProjectDescriptionMaxChars) ||
		exceedsRuneLimit(normalized.Color, conversationProjectMetaMaxChars) ||
		exceedsRuneLimit(normalized.Icon, conversationProjectMetaMaxChars) {
		return ConversationProjectInput{}, ErrInvalidConversationProject
	}
	return normalized, nil
}

func normalizeConversationProjectPatch(input ConversationProjectPatchInput) (model.ConversationProjectPatch, error) {
	var patch model.ConversationProjectPatch
	if input.Name != nil {
		value := strings.TrimSpace(*input.Name)
		if value == "" || exceedsRuneLimit(value, conversationProjectNameMaxChars) {
			return model.ConversationProjectPatch{}, ErrInvalidConversationProject
		}
		patch.Name = &value
	}
	if input.Description != nil {
		value := strings.TrimSpace(*input.Description)
		if exceedsRuneLimit(value, conversationProjectDescriptionMaxChars) {
			return model.ConversationProjectPatch{}, ErrInvalidConversationProject
		}
		patch.Description = &value
	}
	if input.Color != nil {
		value := strings.TrimSpace(*input.Color)
		if exceedsRuneLimit(value, conversationProjectMetaMaxChars) {
			return model.ConversationProjectPatch{}, ErrInvalidConversationProject
		}
		patch.Color = &value
	}
	if input.Icon != nil {
		value := strings.TrimSpace(*input.Icon)
		if exceedsRuneLimit(value, conversationProjectMetaMaxChars) {
			return model.ConversationProjectPatch{}, ErrInvalidConversationProject
		}
		patch.Icon = &value
	}
	if input.Status != nil {
		value := normalizeConversationProjectStatus(*input.Status)
		if value == "" {
			return model.ConversationProjectPatch{}, ErrInvalidConversationProject
		}
		patch.Status = &value
	}
	if patch.Name == nil && patch.Description == nil && patch.Color == nil && patch.Icon == nil && patch.Status == nil {
		return model.ConversationProjectPatch{}, ErrInvalidConversationProject
	}
	return patch, nil
}

func normalizeProjectPublicIDs(values []string) []string {
	results := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		normalized := strings.TrimSpace(value)
		if normalized == "" {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		results = append(results, normalized)
	}
	return results
}

func normalizeConversationProjectStatusFilter(value string) string {
	switch normalizeConversationProjectStatus(value) {
	case "archived":
		return "archived"
	case "active":
		return "active"
	default:
		if strings.TrimSpace(value) == "all" {
			return "all"
		}
		return "active"
	}
}

func normalizeConversationProjectStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "active":
		return "active"
	case "archived":
		return "archived"
	default:
		return ""
	}
}

func normalizeConversationProjectFilter(value string) string {
	normalized := strings.TrimSpace(value)
	switch normalized {
	case "", "all":
		return "all"
	case "unassigned":
		return "unassigned"
	default:
		return normalized
	}
}

func exceedsRuneLimit(value string, limit int) bool {
	return limit >= 0 && utf8.RuneCountInString(value) > limit
}
