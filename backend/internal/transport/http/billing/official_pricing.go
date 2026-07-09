package billing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/shared/response"
	"github.com/DEEIX-AI/DEEIX-Chat/backend/internal/shared/security"
	"github.com/gin-gonic/gin"
)

const (
	openRouterModelsURL           = "https://openrouter.ai/api/v1/models"
	openRouterPricingCacheRelPath = "admin/openrouter-model-pricing.json"
	openRouterPricingCacheTTL     = 24 * time.Hour
)

type openRouterOfficialPricingCacheFile struct {
	FetchedAt time.Time                               `json:"fetchedAt"`
	Items     []OpenRouterOfficialPricingItemResponse `json:"items"`
}

type openRouterModelsResponse struct {
	Data []openRouterModelItem `json:"data"`
}

type openRouterModelItem struct {
	ID            string                 `json:"id"`
	CanonicalSlug string                 `json:"canonical_slug"`
	Name          string                 `json:"name"`
	Pricing       openRouterModelPricing `json:"pricing"`
}

type openRouterModelPricing struct {
	Prompt          string `json:"prompt"`
	Completion      string `json:"completion"`
	InputCacheRead  string `json:"input_cache_read"`
	InputCacheWrite string `json:"input_cache_write"`
}

// GetOpenRouterOfficialPricing godoc
// @Summary 管理员获取 OpenRouter 官方模型定价
// @Description 从 storage 缓存读取 OpenRouter 模型定价；缓存不存在、过期或 refresh=true 时由后端刷新。
// @Tags admin-billing
// @Accept json
// @Produce json
// @Security BearerAuth
// @Param refresh query bool false "强制刷新缓存"
// @Success 200 {object} OpenRouterOfficialPricingResponseDoc
// @Failure 502 {object} ErrorDoc
// @Router /admin/billing/official-pricing/openrouter [get]
func (h *Handler) GetOpenRouterOfficialPricing(c *gin.Context) {
	refresh := strings.EqualFold(strings.TrimSpace(c.Query("refresh")), "true")
	cache, cacheOK := h.readOpenRouterOfficialPricingCache()
	respondWithStaleCache := func() {
		response.Success(c, OpenRouterOfficialPricingDataResponse{
			FetchedAt: cache.FetchedAt,
			Cached:    true,
			Stale:     true,
			Items:     cache.Items,
		})
	}
	if cacheOK && !refresh && !openRouterOfficialPricingCacheStale(cache.FetchedAt) {
		response.Success(c, OpenRouterOfficialPricingDataResponse{
			FetchedAt: cache.FetchedAt,
			Cached:    true,
			Stale:     false,
			Items:     cache.Items,
		})
		return
	}

	items, err := h.fetchOpenRouterOfficialPricing(c.Request.Context())
	if err != nil {
		if cacheOK {
			// 官方价格只用于管理员辅助填充；远程失败时优先保留可用旧缓存，避免阻塞已有配置流程。
			respondWithStaleCache()
			return
		}
		response.Error(c, http.StatusBadGateway, "fetch openrouter official pricing failed")
		return
	}
	if len(items) == 0 {
		if cacheOK {
			respondWithStaleCache()
			return
		}
		response.Error(c, http.StatusBadGateway, "openrouter official pricing is empty")
		return
	}

	nextCache := openRouterOfficialPricingCacheFile{
		FetchedAt: time.Now().UTC(),
		Items:     items,
	}
	if writeErr := h.writeOpenRouterOfficialPricingCache(nextCache); writeErr != nil {
		response.Error(c, http.StatusInternalServerError, "cache openrouter official pricing failed")
		return
	}
	response.Success(c, OpenRouterOfficialPricingDataResponse{
		FetchedAt: nextCache.FetchedAt,
		Cached:    false,
		Stale:     false,
		Items:     nextCache.Items,
	})
}

func (h *Handler) openRouterOfficialPricingCachePath() string {
	root := ""
	if h.cfg != nil {
		root = strings.TrimSpace(h.cfg.Snapshot().StorageRootDir)
	}
	if root == "" {
		root = "./storage"
	}
	return filepath.Join(root, filepath.FromSlash(openRouterPricingCacheRelPath))
}

func (h *Handler) readOpenRouterOfficialPricingCache() (openRouterOfficialPricingCacheFile, bool) {
	path := h.openRouterOfficialPricingCachePath()
	raw, err := os.ReadFile(path)
	if err != nil {
		return openRouterOfficialPricingCacheFile{}, false
	}
	var cache openRouterOfficialPricingCacheFile
	if err := json.Unmarshal(raw, &cache); err != nil || cache.FetchedAt.IsZero() || len(cache.Items) == 0 {
		return openRouterOfficialPricingCacheFile{}, false
	}
	return cache, true
}

func (h *Handler) writeOpenRouterOfficialPricingCache(cache openRouterOfficialPricingCacheFile) error {
	path := h.openRouterOfficialPricingCachePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	tmpPath := fmt.Sprintf("%s.tmp.%d", path, time.Now().UnixNano())
	if err := os.WriteFile(tmpPath, raw, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return nil
}

func (h *Handler) fetchOpenRouterOfficialPricing(ctx context.Context) ([]OpenRouterOfficialPricingItemResponse, error) {
	env := ""
	ssrfProtectionEnabled := false
	if h.cfg != nil {
		cfg := h.cfg.Snapshot()
		env = cfg.Env
		ssrfProtectionEnabled = cfg.SSRFProtectionEnabled
	}
	client := security.NewOutboundHTTPClient(env, ssrfProtectionEnabled, 15*time.Second)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, openRouterModelsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("openrouter models request failed: %d", resp.StatusCode)
	}
	var payload openRouterModelsResponse
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 8<<20))
	if err := decoder.Decode(&payload); err != nil {
		return nil, err
	}
	items := make([]OpenRouterOfficialPricingItemResponse, 0, len(payload.Data))
	for _, item := range payload.Data {
		normalized := normalizeOpenRouterOfficialPricingItem(item)
		if normalized.ID != "" {
			items = append(items, normalized)
		}
	}
	if len(items) == 0 {
		return nil, errors.New("openrouter model list is empty")
	}
	return items, nil
}

func normalizeOpenRouterOfficialPricingItem(item openRouterModelItem) OpenRouterOfficialPricingItemResponse {
	id := strings.TrimSpace(item.ID)
	if id == "" {
		return OpenRouterOfficialPricingItemResponse{}
	}
	canonicalSlug := strings.TrimSpace(item.CanonicalSlug)
	if canonicalSlug == "" {
		canonicalSlug = id
	}
	name := strings.TrimSpace(item.Name)
	if name == "" {
		name = id
	}
	return OpenRouterOfficialPricingItemResponse{
		ID:            id,
		CanonicalSlug: canonicalSlug,
		Name:          name,
		Pricing: OpenRouterOfficialPricingUnitPricingResponse{
			Prompt:          strings.TrimSpace(item.Pricing.Prompt),
			Completion:      strings.TrimSpace(item.Pricing.Completion),
			InputCacheRead:  strings.TrimSpace(item.Pricing.InputCacheRead),
			InputCacheWrite: strings.TrimSpace(item.Pricing.InputCacheWrite),
		},
	}
}

func openRouterOfficialPricingCacheStale(fetchedAt time.Time) bool {
	return fetchedAt.IsZero() || time.Since(fetchedAt) > openRouterPricingCacheTTL
}
