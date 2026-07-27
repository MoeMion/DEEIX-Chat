package mcp

import (
	"context"
	"errors"
	"time"

	domainmcp "github.com/DEEIX-AI/DEEIX-Chat/backend/internal/domain/mcp"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/infra/persistence/models"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/repository"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type Repo struct {
	db *gorm.DB
}

func NewRepo(db *gorm.DB) *Repo {
	return &Repo{db: db}
}

func (r *Repo) CreateServer(ctx context.Context, input repository.CreateMCPServerInput) (*domainmcp.Server, error) {
	var result domainmcp.Server
	if err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var maxSortOrder int
		if err := tx.Model(&model.MCPServer{}).
			Select("COALESCE(MAX(sort_order), 0)").
			Scan(&maxSortOrder).Error; err != nil {
			return err
		}
		item := model.MCPServer{
			PublicID:           input.PublicID,
			ContextJWTAudience: input.ContextJWTAudience,
			Name:               input.Name,
			BaseURL:            input.BaseURL,
			AuthTokenEnc:       input.AuthTokenEnc,
			HeadersJSON:        input.HeadersJSON,
			HeadersEnabled:     input.HeadersEnabled,
			Status:             input.Status,
			SortOrder:          maxSortOrder + 100,
		}
		if err := tx.Create(&item).Error; err != nil {
			return err
		}
		if err := tx.Model(&item).
			UpdateColumn("headers_enabled", input.HeadersEnabled).Error; err != nil {
			return err
		}
		item.HeadersEnabled = input.HeadersEnabled
		result = toDomainServer(item)
		return nil
	}); err != nil {
		return nil, err
	}
	return &result, nil
}

func (r *Repo) UpdateServer(ctx context.Context, serverID uint, input repository.UpdateMCPServerInput) (*domainmcp.Server, error) {
	updates := map[string]interface{}{}
	if input.Name != nil {
		updates["name"] = *input.Name
	}
	if input.BaseURL != nil {
		updates["base_url"] = *input.BaseURL
	}
	if input.AuthTokenEnc != nil {
		updates["auth_token_enc"] = *input.AuthTokenEnc
	}
	if input.HeadersJSON != nil {
		updates["headers_json"] = *input.HeadersJSON
	}
	if input.HeadersEnabled != nil {
		updates["headers_enabled"] = *input.HeadersEnabled
	}
	if input.Status != nil {
		updates["status"] = *input.Status
	}
	if input.LastError != nil {
		updates["last_error"] = *input.LastError
	}
	if len(updates) > 0 {
		result := r.db.WithContext(ctx).Model(&model.MCPServer{}).Where("id = ?", serverID).Updates(updates)
		if result.Error != nil {
			return nil, translateNotFound(result.Error)
		}
		if result.RowsAffected == 0 {
			return nil, repository.ErrNotFound
		}
	}
	return r.GetServer(ctx, serverID)
}

func (r *Repo) UpdateContextJWTPolicy(
	ctx context.Context,
	serverID uint,
	input repository.UpdateMCPContextJWTPolicyInput,
) (*domainmcp.Server, error) {
	result := r.db.WithContext(ctx).Model(&model.MCPServer{}).Where("id = ?", serverID).Updates(map[string]interface{}{
		"context_jwt_expires_seconds": input.ExpiresSeconds,
		"context_jwt_include_name":    input.IncludeName,
		"context_jwt_include_email":   input.IncludeEmail,
		"context_jwt_include_role":    input.IncludeRole,
	})
	if result.Error != nil {
		return nil, result.Error
	}
	return r.GetServer(ctx, serverID)
}

func (r *Repo) PrepareContextJWTRotation(
	ctx context.Context,
	input repository.PrepareMCPContextJWTRotationInput,
) (*domainmcp.Server, error) {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.MCPServer{}).
			Where(
				`id = ? AND (
					context_jwt_pending_secret_enc = '' OR
					context_jwt_pending_key_id = '' OR
					context_jwt_pending_expires_at IS NULL OR
					context_jwt_pending_expires_at <= ?
				)`,
				input.ServerID,
				input.CreatedAt,
			).
			Updates(map[string]interface{}{
				"context_jwt_pending_secret_enc": input.PendingSecretEnc,
				"context_jwt_pending_key_id":     input.PendingKeyID,
				"context_jwt_pending_created_at": input.CreatedAt,
				"context_jwt_pending_expires_at": input.ExpiresAt,
			})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 {
			return nil
		}

		row, err := getMCPServerRow(tx, input.ServerID)
		if err != nil {
			return err
		}
		if row.ContextJWTPendingSecretEnc != "" && row.ContextJWTPendingKeyID != "" &&
			row.ContextJWTPendingExpiresAt != nil && row.ContextJWTPendingExpiresAt.After(input.CreatedAt) {
			return repository.ErrMCPContextJWTPendingExists
		}
		return repository.ErrMCPContextJWTRotationConflict
	})
	if err != nil {
		return nil, err
	}
	return r.GetServer(ctx, input.ServerID)
}

func (r *Repo) ActivateContextJWTRotation(
	ctx context.Context,
	serverID uint,
	kid string,
	now time.Time,
) (*domainmcp.Server, error) {
	var snapshot model.MCPServer
	if err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		row, err := getMCPServerRow(tx, serverID)
		if err != nil {
			return err
		}
		snapshot = row
		return nil
	}); err != nil {
		return nil, err
	}
	if snapshot.ContextJWTPendingKeyID != kid || snapshot.ContextJWTPendingSecretEnc == "" ||
		snapshot.ContextJWTPendingExpiresAt == nil {
		return nil, repository.ErrMCPContextJWTRotationConflict
	}

	if snapshot.ContextJWTPendingExpiresAt.After(now) {
		err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			activateUpdates := clearContextJWTPendingUpdates()
			activateUpdates["context_jwt_mode"] = "hs256"
			activateUpdates["context_jwt_secret_enc"] = gorm.Expr("context_jwt_pending_secret_enc")
			activateUpdates["context_jwt_key_id"] = gorm.Expr("context_jwt_pending_key_id")
			result := tx.Model(&model.MCPServer{}).
				Where(
					`id = ? AND context_jwt_pending_key_id = ? AND
					context_jwt_pending_secret_enc <> '' AND
					context_jwt_pending_expires_at IS NOT NULL AND
					context_jwt_pending_expires_at > ?`,
					serverID,
					kid,
					now,
				).
				Updates(activateUpdates)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 1 {
				return nil
			}
			if _, err := getMCPServerRow(tx, serverID); err != nil {
				return err
			}
			return repository.ErrMCPContextJWTRotationConflict
		})
		if err != nil {
			return nil, err
		}
		return r.GetServer(ctx, serverID)
	}

	expiresAtSnapshot := *snapshot.ContextJWTPendingExpiresAt
	expired := false
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		cleanup := tx.Model(&model.MCPServer{}).
			Where(
				`id = ? AND context_jwt_pending_key_id = ? AND
				context_jwt_pending_expires_at = ? AND context_jwt_pending_expires_at <= ?`,
				serverID,
				kid,
				expiresAtSnapshot,
				now,
			).
			Updates(clearContextJWTPendingUpdates())
		if cleanup.Error != nil {
			return cleanup.Error
		}
		if cleanup.RowsAffected == 1 {
			expired = true
			return nil
		}
		if _, err := getMCPServerRow(tx, serverID); err != nil {
			return err
		}
		return repository.ErrMCPContextJWTRotationConflict
	})
	if err != nil {
		return nil, err
	}
	if expired {
		return nil, repository.ErrMCPContextJWTPendingExpired
	}
	return r.GetServer(ctx, serverID)
}

func (r *Repo) CancelContextJWTRotation(ctx context.Context, serverID uint, kid string) (*domainmcp.Server, error) {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		result := tx.Model(&model.MCPServer{}).
			Where("id = ? AND context_jwt_pending_key_id = ?", serverID, kid).
			Updates(clearContextJWTPendingUpdates())
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 {
			return nil
		}
		if _, err := getMCPServerRow(tx, serverID); err != nil {
			return err
		}
		return repository.ErrMCPContextJWTRotationConflict
	})
	if err != nil {
		return nil, err
	}
	return r.GetServer(ctx, serverID)
}

func (r *Repo) DisableContextJWT(ctx context.Context, serverID uint) (*domainmcp.Server, error) {
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		updates := clearContextJWTPendingUpdates()
		updates["context_jwt_mode"] = "none"
		updates["context_jwt_secret_enc"] = ""
		updates["context_jwt_key_id"] = ""
		updates["context_jwt_include_name"] = false
		updates["context_jwt_include_email"] = false
		updates["context_jwt_include_role"] = false
		result := tx.Model(&model.MCPServer{}).Where("id = ?", serverID).Updates(updates)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 1 {
			return nil
		}
		if _, err := getMCPServerRow(tx, serverID); err != nil {
			return err
		}
		return repository.ErrMCPContextJWTRotationConflict
	})
	if err != nil {
		return nil, err
	}
	return r.GetServer(ctx, serverID)
}

func (r *Repo) ClearExpiredContextJWTPending(ctx context.Context, now time.Time) error {
	return r.db.WithContext(ctx).
		Model(&model.MCPServer{}).
		Where("context_jwt_pending_expires_at IS NOT NULL AND context_jwt_pending_expires_at <= ?", now).
		Updates(clearContextJWTPendingUpdates()).Error
}

func (r *Repo) ListServers(ctx context.Context) ([]domainmcp.Server, error) {
	return listServers(ctx, r.db)
}

func listServers(ctx context.Context, db *gorm.DB) ([]domainmcp.Server, error) {
	var rows []model.MCPServer
	if err := db.WithContext(ctx).Order("sort_order asc").Order("id asc").Find(&rows).Error; err != nil {
		return nil, err
	}
	activeCounts := map[uint]int{}
	if len(rows) > 0 {
		serverIDs := make([]uint, 0, len(rows))
		for _, row := range rows {
			serverIDs = append(serverIDs, row.ID)
		}
		var counts []struct {
			ServerID uint
			Count    int
		}
		if err := db.WithContext(ctx).
			Model(&model.MCPTool{}).
			Select("server_id, count(*) as count").
			Where("server_id IN ? AND status = ?", serverIDs, "active").
			Group("server_id").
			Scan(&counts).Error; err != nil {
			return nil, err
		}
		for _, item := range counts {
			activeCounts[item.ServerID] = item.Count
		}
	}
	items := make([]domainmcp.Server, 0, len(rows))
	for _, row := range rows {
		item := toDomainServer(row)
		item.ActiveToolCount = activeCounts[row.ID]
		items = append(items, item)
	}
	return items, nil
}

func (r *Repo) GetServer(ctx context.Context, serverID uint) (*domainmcp.Server, error) {
	var row model.MCPServer
	if err := r.db.WithContext(ctx).First(&row, "id = ?", serverID).Error; err != nil {
		return nil, translateNotFound(err)
	}
	item := toDomainServer(row)
	return &item, nil
}

func (r *Repo) DeleteServer(ctx context.Context, serverID uint) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		toolIDs := make([]uint, 0)
		if err := tx.Model(&model.MCPTool{}).Where("server_id = ?", serverID).Pluck("id", &toolIDs).Error; err != nil {
			return err
		}
		if err := deleteConversationProjectMCPToolAssociations(tx, toolIDs); err != nil {
			return err
		}
		if err := tx.Where("server_id = ?", serverID).Delete(&model.MCPTool{}).Error; err != nil {
			return err
		}
		result := tx.Delete(&model.MCPServer{}, "id = ?", serverID)
		if result.Error != nil {
			return translateNotFound(result.Error)
		}
		if result.RowsAffected == 0 {
			return repository.ErrNotFound
		}
		return nil
	})
}

func (r *Repo) ReplaceServerTools(ctx context.Context, serverID uint, tools []domainmcp.Tool) error {
	now := time.Now()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var maxSortOrder int
		if err := tx.Model(&model.MCPTool{}).
			Where("server_id = ?", serverID).
			Select("COALESCE(MAX(sort_order), 0)").
			Scan(&maxSortOrder).Error; err != nil {
			return err
		}
		rows := make([]model.MCPTool, 0, len(tools))
		names := make([]string, 0, len(tools))
		for index, tool := range tools {
			names = append(names, tool.Name)
			rows = append(rows, model.MCPTool{
				ServerID:        serverID,
				Name:            tool.Name,
				DisplayName:     tool.DisplayName,
				Description:     tool.Description,
				InputSchemaJSON: tool.InputSchemaJSON,
				Status:          tool.Status,
				SortOrder:       maxSortOrder + (index+1)*100,
			})
		}
		if len(rows) > 0 {
			if err := tx.Clauses(clause.OnConflict{
				Columns: []clause.Column{{Name: "server_id"}, {Name: "name"}},
				DoUpdates: clause.AssignmentColumns([]string{
					"input_schema_json",
					"updated_at",
				}),
			}).Create(&rows).Error; err != nil {
				return err
			}
		}
		staleToolIDs := make([]uint, 0)
		staleToolQuery := tx.Model(&model.MCPTool{}).Where("server_id = ?", serverID)
		if len(names) > 0 {
			staleToolQuery = staleToolQuery.Where("name NOT IN ?", names)
		}
		if err := staleToolQuery.Pluck("id", &staleToolIDs).Error; err != nil {
			return err
		}
		if err := deleteConversationProjectMCPToolAssociations(tx, staleToolIDs); err != nil {
			return err
		}
		deleteQuery := tx.Where("server_id = ?", serverID)
		if len(names) > 0 {
			deleteQuery = deleteQuery.Where("name NOT IN ?", names)
		}
		if err := deleteQuery.Delete(&model.MCPTool{}).Error; err != nil {
			return err
		}
		return tx.Model(&model.MCPServer{}).Where("id = ?", serverID).Updates(map[string]interface{}{
			"tool_count":     len(tools),
			"last_synced_at": &now,
			"last_error":     "",
		}).Error
	})
}

// deleteConversationProjectMCPToolAssociations 清理已删除工具的项目默认关联。
func deleteConversationProjectMCPToolAssociations(tx *gorm.DB, toolIDs []uint) error {
	if len(toolIDs) == 0 {
		return nil
	}
	return tx.Where("tool_id IN ?", toolIDs).Delete(&model.ConversationProjectMCPTool{}).Error
}

func (r *Repo) ListTools(ctx context.Context, serverID uint, onlyActive bool) ([]domainmcp.Tool, error) {
	query := r.db.WithContext(ctx).Where("server_id = ?", serverID).Order("sort_order asc").Order("name asc").Order("id asc")
	if onlyActive {
		query = query.Where("status = ?", "active")
	}
	var rows []model.MCPTool
	if err := query.Find(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]domainmcp.Tool, 0, len(rows))
	for _, row := range rows {
		items = append(items, toDomainTool(row))
	}
	return items, nil
}

func (r *Repo) ListToolsByIDs(ctx context.Context, toolIDs []uint) ([]domainmcp.Tool, error) {
	if len(toolIDs) == 0 {
		return []domainmcp.Tool{}, nil
	}
	var rows []model.MCPTool
	if err := r.db.WithContext(ctx).
		Joins("JOIN mcp_servers ON mcp_servers.id = mcp_tools.server_id").
		Where("mcp_tools.id IN ?", toolIDs).
		Order("mcp_servers.sort_order asc").
		Order("mcp_servers.id asc").
		Order("mcp_tools.sort_order asc").
		Order("mcp_tools.name asc").
		Order("mcp_tools.id asc").
		Find(&rows).Error; err != nil {
		return nil, err
	}
	items := make([]domainmcp.Tool, 0, len(rows))
	for _, row := range rows {
		items = append(items, toDomainTool(row))
	}
	return items, nil
}

func (r *Repo) UpdateTool(ctx context.Context, toolID uint, input repository.UpdateMCPToolInput) (*domainmcp.Tool, error) {
	updates := map[string]interface{}{}
	if input.DisplayName != nil {
		updates["display_name"] = *input.DisplayName
	}
	if input.Description != nil {
		updates["description"] = *input.Description
	}
	if input.Status != nil {
		updates["status"] = *input.Status
	}
	if len(updates) > 0 {
		if err := r.db.WithContext(ctx).Model(&model.MCPTool{}).Where("id = ?", toolID).Updates(updates).Error; err != nil {
			return nil, err
		}
	}
	var row model.MCPTool
	if err := r.db.WithContext(ctx).First(&row, "id = ?", toolID).Error; err != nil {
		return nil, err
	}
	item := toDomainTool(row)
	return &item, nil
}

func (r *Repo) UpdateServerToolsStatus(ctx context.Context, serverID uint, toolIDs []uint, status string) ([]domainmcp.Tool, error) {
	if err := r.db.WithContext(ctx).
		Model(&model.MCPTool{}).
		Where("server_id = ? AND id IN ?", serverID, toolIDs).
		Update("status", status).Error; err != nil {
		return nil, err
	}
	return r.ListTools(ctx, serverID, false)
}

func (r *Repo) ReorderServersWithTools(ctx context.Context, order []repository.ReorderMCPServerInput) ([]domainmcp.ServerWithTools, error) {
	if len(order) == 0 {
		return []domainmcp.ServerWithTools{}, nil
	}
	returned := make([]domainmcp.ServerWithTools, 0)
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		serverIDs := make([]uint, 0, len(order))
		for _, item := range order {
			serverIDs = append(serverIDs, item.ServerID)
		}
		var existingServers []model.MCPServer
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id IN ?", serverIDs).
			Find(&existingServers).Error; err != nil {
			return err
		}
		if len(existingServers) != len(order) {
			return gorm.ErrRecordNotFound
		}
		seenServers := make(map[uint]struct{}, len(order))
		for index, item := range order {
			if _, ok := seenServers[item.ServerID]; ok {
				return gorm.ErrRecordNotFound
			}
			seenServers[item.ServerID] = struct{}{}
			sortOrder := (index + 1) * 100
			if err := tx.Model(&model.MCPServer{}).
				Where("id = ?", item.ServerID).
				Update("sort_order", sortOrder).Error; err != nil {
				return err
			}
			var existingTools []model.MCPTool
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
				Where("server_id = ?", item.ServerID).
				Find(&existingTools).Error; err != nil {
				return err
			}
			if len(existingTools) != len(item.ToolIDs) {
				return gorm.ErrRecordNotFound
			}
			allowedTools := make(map[uint]struct{}, len(existingTools))
			for _, tool := range existingTools {
				allowedTools[tool.ID] = struct{}{}
			}
			seenTools := make(map[uint]struct{}, len(item.ToolIDs))
			for toolIndex, toolID := range item.ToolIDs {
				if _, ok := allowedTools[toolID]; !ok {
					return gorm.ErrRecordNotFound
				}
				if _, ok := seenTools[toolID]; ok {
					return gorm.ErrRecordNotFound
				}
				seenTools[toolID] = struct{}{}
				toolSortOrder := (toolIndex + 1) * 100
				if err := tx.Model(&model.MCPTool{}).
					Where("server_id = ? AND id = ?", item.ServerID, toolID).
					Update("sort_order", toolSortOrder).Error; err != nil {
					return err
				}
			}
		}

		servers, err := listServers(ctx, tx)
		if err != nil {
			return err
		}
		returned = make([]domainmcp.ServerWithTools, 0, len(servers))
		for _, server := range servers {
			var rows []model.MCPTool
			if err := tx.Where("server_id = ?", server.ID).
				Order("sort_order asc").
				Order("name asc").
				Order("id asc").
				Find(&rows).Error; err != nil {
				return err
			}
			tools := make([]domainmcp.Tool, 0, len(rows))
			for _, row := range rows {
				tool := toDomainTool(row)
				tool.ServerName = server.Name
				tools = append(tools, tool)
			}
			returned = append(returned, domainmcp.ServerWithTools{
				Server: server,
				Tools:  tools,
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return returned, nil
}

func clearContextJWTPendingUpdates() map[string]interface{} {
	return map[string]interface{}{
		"context_jwt_pending_secret_enc": "",
		"context_jwt_pending_key_id":     "",
		"context_jwt_pending_created_at": nil,
		"context_jwt_pending_expires_at": nil,
	}
}

func getMCPServerRow(db *gorm.DB, serverID uint) (model.MCPServer, error) {
	var row model.MCPServer
	if err := db.First(&row, "id = ?", serverID).Error; err != nil {
		return model.MCPServer{}, translateNotFound(err)
	}
	return row, nil
}

func translateNotFound(err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return repository.ErrNotFound
	}
	return err
}

func toDomainServer(row model.MCPServer) domainmcp.Server {
	return domainmcp.Server{
		ID:                         row.ID,
		PublicID:                   row.PublicID,
		Name:                       row.Name,
		BaseURL:                    row.BaseURL,
		AuthTokenEnc:               row.AuthTokenEnc,
		HeadersJSON:                row.HeadersJSON,
		HeadersEnabled:             row.HeadersEnabled,
		Status:                     row.Status,
		SortOrder:                  row.SortOrder,
		ToolCount:                  row.ToolCount,
		ActiveToolCount:            0,
		LastSyncedAt:               row.LastSyncedAt,
		LastError:                  row.LastError,
		ContextJWTMode:             row.ContextJWTMode,
		ContextJWTSecretEnc:        row.ContextJWTSecretEnc,
		ContextJWTAudience:         row.ContextJWTAudience,
		ContextJWTKeyID:            row.ContextJWTKeyID,
		ContextJWTExpiresSeconds:   row.ContextJWTExpiresSeconds,
		ContextJWTIncludeName:      row.ContextJWTIncludeName,
		ContextJWTIncludeEmail:     row.ContextJWTIncludeEmail,
		ContextJWTIncludeRole:      row.ContextJWTIncludeRole,
		ContextJWTPendingSecretEnc: row.ContextJWTPendingSecretEnc,
		ContextJWTPendingKeyID:     row.ContextJWTPendingKeyID,
		ContextJWTPendingCreatedAt: row.ContextJWTPendingCreatedAt,
		ContextJWTPendingExpiresAt: row.ContextJWTPendingExpiresAt,
		CreatedAt:                  row.CreatedAt,
		UpdatedAt:                  row.UpdatedAt,
	}
}

func toDomainTool(row model.MCPTool) domainmcp.Tool {
	return domainmcp.Tool{
		ID:              row.ID,
		ServerID:        row.ServerID,
		Name:            row.Name,
		DisplayName:     row.DisplayName,
		Description:     row.Description,
		InputSchemaJSON: row.InputSchemaJSON,
		Status:          row.Status,
		SortOrder:       row.SortOrder,
		CreatedAt:       row.CreatedAt,
		UpdatedAt:       row.UpdatedAt,
	}
}
