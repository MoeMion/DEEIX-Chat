# 代码审查报告

**分支：** `feat_collapse_recent` vs `origin/main`  
**日期：** 2026-06-16  
**涉及文件：** 13 个（后端 4 个 · 前端 9 个）

---

## 问题一 — [已确认 · 严重] 折叠后重新展开导致无限滚动永久失效

**文件：** `frontend/features/layouts/components/navigation/nav-recents.tsx`

Radix `CollapsibleContent` 在未设置 `forceMount` 时，默认会**卸载**折叠区域的子节点。折叠时 `loadMoreRef.current` 变为 `null`；重新展开时 `enabled` 翻回 `true` 触发 `useLoadMoreSentinel` 的 effect，但此时 DOM 节点尚未挂载，effect 内部的 `if (!target) return` 提前退出，`IntersectionObserver` 永远注册不上。结果：**用户折叠再展开"最近对话"区块后，本次会话的无限滚动彻底失效**，直到刷新页面。

**修复建议：** 给 `CollapsibleContent` 传 `forceMount`（保留 DOM 节点）并通过 CSS 控制可见性；或在 `useLoadMoreSentinel` 中改用回调 ref（`ref={node => { loadMoreRef.current = node }}`），让 observer 在节点挂载时自动注册。

---

## 问题二 — [已确认 · 中等] 存储"折叠"偏好的用户每次刷新看到展开→折叠闪烁

**文件：** `frontend/features/layouts/components/navigation/nav-recents.tsx:84` · `nav-starred.tsx:101`

两个组件用 `useEffect`（绘制后异步执行）读取 `localStorage`，而初始状态固定为 `true`（展开）。对于上次选择折叠的用户，每次刷新都会出现"先展开一帧再折叠"的视觉闪烁。对比 `sidebar.tsx` 的正确做法：

```ts
// sidebar.tsx — 正确：在 useState 初始化时同步读取
const [_open, _setOpen] = React.useState(
  () => resolveSidebarPreferredOpen(defaultOpen) && !isCompactSidebarViewport()
)
```

**修复建议：** 将 `nav-recents.tsx` 和 `nav-starred.tsx` 的初始状态改为懒初始化方式同步读取 localStorage，与 `sidebar.tsx` 保持一致：

```ts
const [recentsOpen, setRecentsOpen] = React.useState(() => {
  try {
    const s = window.localStorage.getItem(RECENTS_OPEN_STORAGE_KEY)
    if (s === "false") return false
  } catch {}
  return true
})
```

---

## 问题三 — [可能存在 · 中等] 删除 `!defaultOpen` 守卫引入潜在自动展开逻辑缺口

**文件：** `frontend/components/ui/sidebar.tsx:163`

```diff
-    if (!leftCompactViewport || !autoCollapsedRef.current || !defaultOpen) {
+    if (!leftCompactViewport || !autoCollapsedRef.current) {
```

若某调用方将来以 `defaultOpen={false}` 挂载侧边栏，但 localStorage 中仍存有上次 `"true"` 的记录，则 `resolveSidebarPreferredOpen(false)` 会返回 `true`，导致 `autoCollapsedRef.current` 初始化为 `true`。此时删掉的守卫正好会阻止自动展开，而现在已无法阻止。当前代码库没有传 `false` 的调用方，属于潜在风险，但值得记录。

---

## 问题四 — [可能存在 · 低] 头像异步保存期间组件卸载导致无效 setState

**文件：** `frontend/features/settings/components/sections/general/settings-general.tsx:271`

`handleSaveAvatarDialog` 改为 `async`，`await patchMe(...)` 期间若用户导航离开，`finally` 块中的 `setSaving / setViewer / setDraft` 等调用会作用于已卸载的组件。React 18 下这通常是无害的警告，但在并发模式下可能将过期闭包中捕获的 `accessToken`、`initialDraft.avatarUrl` 写回，影响后续挂载的新实例。建议添加 `AbortController` 或 `isMounted` 守卫。

---

## 清理建议 — `nav-recents` 与 `nav-starred` 水合逻辑完全重复

两个文件各有一对结构完全相同的 `useEffect`（读取 localStorage + 写回守卫），变量名不同但逻辑相同。建议提取为共享 hook，例如 `useLocalStorageBoolean(key, defaultValue)`，同时可顺带解决上述问题二。

---

## 总结

| # | 严重程度 | 状态 | 描述 |
|---|----------|------|------|
| 1 | 严重 | 已确认 | 折叠→展开后无限滚动永久失效 |
| 2 | 中等 | 已确认 | 存储折叠偏好的用户刷新时出现展开闪烁 |
| 3 | 中等 | 可能存在 | 删除 `!defaultOpen` 守卫的潜在自动展开缺口 |
| 4 | 低 | 可能存在 | 头像保存异步期间组件卸载的 setState 风险 |
| — | — | 清理 | nav-recents/nav-starred localStorage 水合逻辑重复 |

最需要立即处理的是**问题一**和**问题二**，两者可通过同步初始化 + `CollapsibleContent` 加 `forceMount` 一并解决。
