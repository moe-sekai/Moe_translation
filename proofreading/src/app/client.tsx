"use client";

import React, { useState, useEffect, useCallback, useRef, useMemo } from "react";
import {
    getToken, setToken, clearToken, getUsername, setUsername,
    login, getCategories, getEntries, updateEntry, pushToHub, getPushStatus,
    triggerAITranslateAll, getEventStories, getEventStory, runCNSync,
    updateEventStoryLine,
    type CategoryInfo, type TranslationEntry, type PushStatus,
    type EventStorySummary, type EventStoryDetail,
} from "@/lib/api";
import { useTheme } from "next-themes";

// ============================================================================
// Labels
// ============================================================================

const CATEGORY_LABELS: Record<string, string> = {
    cards: "卡牌", events: "活动", music: "音乐", gacha: "卡池",
    virtualLive: "虚拟Live", sticker: "贴纸", comic: "漫画",
    mysekai: "我的世界", costumes: "服装", characters: "角色", units: "团体",
};

const FIELD_LABELS: Record<string, string> = {
    prefix: "卡面名称", skillName: "技能名", gachaPhrase: "抽卡台词",
    name: "名称", title: "标题", artist: "音乐人", vocalCaption: "歌手名",
    fixtureName: "家具名", flavorText: "描述文本", genre: "分类", tag: "标签",
    colorName: "配色名", designer: "设计师", hobby: "爱好", specialSkill: "特技",
    favoriteFood: "喜欢的食物", hatedFood: "讨厌的食物", weak: "弱点",
    introduction: "自我介绍", unitName: "团体名", profileSentence: "团体简介",
    subGenre: "子分类", material: "材料",
};

const SOURCE_LABELS: Record<string, string> = {
    cn: "官方", human: "人工", pinned: "锁定", llm: "AI", unknown: "未知",
};

// ============================================================================
// Toast Hook
// ============================================================================

function useToast() {
    const [toasts, setToasts] = useState<{ id: number; msg: string; type: "ok" | "err" }[]>([]);
    const nextId = useRef(0);
    const show = useCallback((msg: string, type: "ok" | "err") => {
        const id = ++nextId.current;
        setToasts(p => [...p, { id, msg, type }]);
        setTimeout(() => setToasts(p => p.filter(t => t.id !== id)), 3000);
    }, []);
    return { toasts, show };
}

// ============================================================================
// Login
// ============================================================================

function LoginPage({ onLogin }: { onLogin: (name: string) => void }) {
    const [user, setUser] = useState("");
    const [pass, setPass] = useState("");
    const [error, setError] = useState("");
    const [loading, setLoading] = useState(false);

    const handleSubmit = async (e: React.FormEvent) => {
        e.preventDefault();
        setError("");
        setLoading(true);
        try {
            const data = await login(user, pass);
            setToken(data.token);
            setUsername(data.username);
            onLogin(data.username);
        } catch (err) {
            setError(err instanceof Error ? err.message : "登录失败");
        } finally {
            setLoading(false);
        }
    };

    return (
        <div className="login-container">
            <form className="login-card" onSubmit={handleSubmit}>
                <h1>翻译校对系统</h1>
                <p>Sekai Translation Proofreading</p>
                {error && <div className="login-error">{error}</div>}
                <input type="text" placeholder="用户名" value={user} onChange={e => setUser(e.target.value)} autoFocus />
                <input type="password" placeholder="密码" value={pass} onChange={e => setPass(e.target.value)} />
                <button type="submit" disabled={loading || !user || !pass}>
                    {loading ? "登录中..." : "登录"}
                </button>
            </form>
        </div>
    );
}

// ============================================================================
// Main Component
// ============================================================================

export default function ProofreadingClient() {
    // Auth
    const [loggedIn, setLoggedIn] = useState<boolean | null>(null);
    const [currentUser, setCurrentUser] = useState("");

    // Data
    const [categories, setCategories] = useState<CategoryInfo[]>([]);
    const [selectedCategory, setSelectedCategory] = useState("");
    const [selectedField, setSelectedField] = useState("");
    const [sourceFilter, setSourceFilter] = useState("");
    const [entries, setEntries] = useState<TranslationEntry[]>([]);
    const [loadingEntries, setLoadingEntries] = useState(false);
    const [searchQuery, setSearchQuery] = useState("");
    const [selectedKey, setSelectedKey] = useState<string | null>(null);

    // Edit
    const [editValue, setEditValue] = useState("");
    const [isEditing, setIsEditing] = useState(false);
    const editRef = useRef<HTMLTextAreaElement>(null);
    const savingRef = useRef(false);

    // Push
    const [pushing, setPushing] = useState(false);
    const [pushStatus, setPushStatus] = useState<PushStatus | null>(null);
    const [syncingCN, setSyncingCN] = useState(false);

    // AI translation
    const [aiProvider, setAIProvider] = useState<"gemini" | "openai">("gemini");
    const [aiTranslating, setAITranslating] = useState(false);

    // Event story block
    const [eventStories, setEventStories] = useState<EventStorySummary[]>([]);
    const [selectedEventId, setSelectedEventId] = useState<number | null>(null);
    const [selectedStoryDetail, setSelectedStoryDetail] = useState<EventStoryDetail | null>(null);
    const [storyLoading, setStoryLoading] = useState(false);

    // Sidebar (mobile)
    const [sidebarOpen, setSidebarOpen] = useState(false);

    // Toast
    const { toasts, show: showToast } = useToast();

    // Theme
    const { theme, setTheme } = useTheme();
    const [mounted, setMounted] = useState(false);

    // Computed
    const filteredEntries = useMemo(() => {
        if (!searchQuery) return entries;
        const q = searchQuery.toLowerCase();
        return entries.filter(e => e.key.toLowerCase().includes(q) || e.text.toLowerCase().includes(q));
    }, [entries, searchQuery]);

    const selectedEntry = useMemo(
        () => filteredEntries.find(e => e.key === selectedKey) ?? null,
        [selectedKey, filteredEntries]
    );

    const selectedIndex = useMemo(
        () => (selectedKey ? filteredEntries.findIndex(e => e.key === selectedKey) : -1),
        [selectedKey, filteredEntries]
    );

    // ---- Auth check on mount ----
    useEffect(() => {
        setMounted(true);
        const token = getToken();
        if (!token) { setLoggedIn(false); return; }
        getCategories()
            .then(cats => { setCategories(cats); setLoggedIn(true); setCurrentUser(getUsername()); })
            .catch(() => { clearToken(); setLoggedIn(false); });
    }, []);

    // ---- Load entries when selection changes ----
    useEffect(() => {
        if (!selectedCategory || !selectedField || !loggedIn) return;
        setLoadingEntries(true);
        setSelectedKey(null);
        setIsEditing(false);

        getEntries(selectedCategory, selectedField, sourceFilter || undefined)
            .then(data => {
                const order: Record<string, number> = { unknown: 0, llm: 1, human: 2, pinned: 3, cn: 4 };
                data.sort((a, b) => (order[a.source] ?? 5) - (order[b.source] ?? 5));
                setEntries(data);
                if (data.length > 0) {
                    setSelectedKey(data[0].key);
                    setEditValue(data[0].text);
                    setIsEditing(false);
                }
            })
            .catch(err => showToast(err.message, "err"))
            .finally(() => setLoadingEntries(false));
    }, [selectedCategory, selectedField, sourceFilter, loggedIn, showToast]);

    // ---- Push status polling ----
    useEffect(() => {
        if (!loggedIn) return;
        const fetch = () => getPushStatus().then(setPushStatus).catch(() => { });
        fetch();
        const iv = setInterval(fetch, 30000);
        return () => clearInterval(iv);
    }, [loggedIn]);

    // ---- Event stories summary ----
    useEffect(() => {
        if (!loggedIn) return;
        getEventStories()
            .then(data => {
                setEventStories(data);
                if (data.length > 0) {
                    setSelectedEventId(prev => prev ?? data[0].eventId);
                }
            })
            .catch(() => {
                setEventStories([]);
            });
    }, [loggedIn]);

    // ---- Event story detail ----
    useEffect(() => {
        if (!loggedIn || !selectedEventId) {
            setSelectedStoryDetail(null);
            return;
        }
        setStoryLoading(true);
        getEventStory(selectedEventId)
            .then(setSelectedStoryDetail)
            .catch(() => setSelectedStoryDetail(null))
            .finally(() => setStoryLoading(false));
    }, [loggedIn, selectedEventId]);

    // ---- Focus textarea on selection ----
    useEffect(() => {
        if (!selectedKey || !editRef.current) return;
        editRef.current.focus();
        requestAnimationFrame(() => {
            if (!editRef.current) return;
            editRef.current.setSelectionRange(0, editRef.current.value.length);
        });
    }, [selectedKey]);

    // ---- Handlers ----

    const handleLogin = (name: string) => {
        setCurrentUser(name);
        setLoggedIn(true);
        getCategories().then(setCategories).catch(err => showToast(err.message, "err"));
    };

    const handleLogout = () => { clearToken(); setLoggedIn(false); setCurrentUser(""); };

    const handleFieldSelect = (cat: string, field: string) => {
        setSelectedCategory(cat);
        setSelectedField(field);
        setSearchQuery("");
        setSelectedKey(null);
        setIsEditing(false);
        setSidebarOpen(false);
    };

    const selectEntry = useCallback((key: string) => {
        setSelectedKey(key);
        setIsEditing(false);
        const entry = entries.find(e => e.key === key);
        if (entry) setEditValue(entry.text);
    }, [entries]);

    const navigateEntry = useCallback((dir: 1 | -1) => {
        if (selectedIndex < 0) return;
        const idx = selectedIndex + dir;
        if (idx < 0 || idx >= filteredEntries.length) return;
        const next = filteredEntries[idx];
        setSelectedKey(next.key);
        setEditValue(next.text);
        setIsEditing(false);
        document.querySelector(`[data-key="${CSS.escape(next.key)}"]`)
            ?.scrollIntoView({ block: "nearest", behavior: "smooth" });
    }, [selectedIndex, filteredEntries]);

    const handleSave = useCallback(async (overrideSource?: string) => {
        if (savingRef.current || !selectedKey || !selectedCategory || !selectedField) return;
        savingRef.current = true;
        const src = overrideSource || "human";

        try {
            const result = await updateEntry(selectedCategory, selectedField, selectedKey, editValue, src);

            // Update local state
            setEntries(prev => prev.map(e =>
                e.key === selectedKey ? { ...e, text: editValue, source: src } : e
            ));

            if (result.status !== "noop") {
                showToast("保存成功", "ok");
            } else {
                showToast("内容未变化", "ok");
            }

            // Move to next entry
            const idx = filteredEntries.findIndex(e => e.key === selectedKey);
            if (idx < filteredEntries.length - 1) {
                const next = filteredEntries[idx + 1];
                setSelectedKey(next.key);
                setEditValue(next.text);
                setIsEditing(false);
            } else {
                showToast("已到最后一条", "ok");
            }
        } catch (err) {
            showToast(err instanceof Error ? err.message : "保存失败", "err");
        } finally {
            savingRef.current = false;
        }
    }, [selectedKey, selectedCategory, selectedField, editValue, filteredEntries, showToast]);

    const handleSourceChange = useCallback(async (key: string, newSource: string) => {
        if (!selectedCategory || !selectedField) return;
        const entry = entries.find(e => e.key === key);
        if (!entry) return;
        try {
            await updateEntry(selectedCategory, selectedField, key, entry.text, newSource);
            setEntries(prev => prev.map(e => e.key === key ? { ...e, source: newSource } : e));
            showToast(`来源已改为「${SOURCE_LABELS[newSource] || newSource}」`, "ok");
        } catch (err) {
            showToast(err instanceof Error ? err.message : "修改失败", "err");
        }
    }, [selectedCategory, selectedField, entries, showToast]);

    const handlePush = async () => {
        setPushing(true);
        try {
            await pushToHub();
            showToast("推送成功", "ok");
            getPushStatus().then(setPushStatus);
        } catch (err) {
            showToast(err instanceof Error ? err.message : "推送失败", "err");
        } finally {
            setPushing(false);
        }
    };

    const handleCNSync = async () => {
        setSyncingCN(true);
        try {
            await runCNSync();
            showToast("数据更新完成", "ok");
            const cats = await getCategories();
            setCategories(cats);
            if (selectedCategory && selectedField) {
                const data = await getEntries(selectedCategory, selectedField, sourceFilter || undefined);
                const order: Record<string, number> = { unknown: 0, llm: 1, human: 2, pinned: 3, cn: 4 };
                data.sort((a, b) => (order[a.source] ?? 5) - (order[b.source] ?? 5));
                setEntries(data);
            }
            const stories = await getEventStories();
            setEventStories(stories);
        } catch (err) {
            showToast(err instanceof Error ? err.message : "数据更新失败", "err");
        } finally {
            setSyncingCN(false);
        }
    };

    const handleAITranslateAll = async () => {
        setAITranslating(true);
        try {
            const result = await triggerAITranslateAll(aiProvider);
            showToast(`AI全局完成: ${result.totalTranslated}/${result.totalCandidates} (${result.totalFields} 字段)`, "ok");
            // Refresh current entries if viewing something
            if (selectedCategory && selectedField) {
                const data = await getEntries(selectedCategory, selectedField, sourceFilter || undefined);
                const order: Record<string, number> = { unknown: 0, llm: 1, human: 2, pinned: 3, cn: 4 };
                data.sort((a, b) => (order[a.source] ?? 5) - (order[b.source] ?? 5));
                setEntries(data);
            }
        } catch (err) {
            showToast(err instanceof Error ? err.message : "AI翻译失败", "err");
        } finally {
            setAITranslating(false);
        }
    };

    const handleStoryLineUpdate = async (eventId: number, episodeNo: string, jpKey: string, cnText: string) => {
        try {
            await updateEventStoryLine(eventId, episodeNo, jpKey, cnText);
            showToast("剧情翻译已保存", "ok");
            // Refresh the story detail
            const detail = await getEventStory(eventId);
            setSelectedStoryDetail(detail);
        } catch (err) {
            showToast(err instanceof Error ? err.message : "保存失败", "err");
        }
    };

    // ---- Keyboard ----

    const handleTextareaKeyDown = useCallback((e: React.KeyboardEvent<HTMLTextAreaElement>) => {
        if (e.key === "Enter" && !e.shiftKey) { e.preventDefault(); handleSave(); }
        if (e.key === "Escape") { e.preventDefault(); setSelectedKey(null); setIsEditing(false); }
        if (e.ctrlKey && e.key === "ArrowUp") { e.preventDefault(); navigateEntry(-1); }
        if (e.ctrlKey && e.key === "ArrowDown") { e.preventDefault(); navigateEntry(1); }
    }, [handleSave, navigateEntry]);

    useEffect(() => {
        const handler = (e: KeyboardEvent) => {
            const tag = (e.target as HTMLElement).tagName;
            if (tag === "INPUT" || tag === "SELECT" || tag === "TEXTAREA") return;
            if (e.ctrlKey && e.key === "s") { e.preventDefault(); handleSave(); }
            if (e.key === "ArrowDown" || e.key === "j") { e.preventDefault(); navigateEntry(1); }
            if (e.key === "ArrowUp" || e.key === "k") { e.preventDefault(); navigateEntry(-1); }
            if (e.key === "Enter" && selectedKey) { e.preventDefault(); editRef.current?.focus(); }
            if (e.key === "Escape") { setSelectedKey(null); setIsEditing(false); }
        };
        window.addEventListener("keydown", handler);
        return () => window.removeEventListener("keydown", handler);
    }, [selectedKey, handleSave, navigateEntry]);

    // ---- Render ----

    if (loggedIn === null) {
        return <div className="page"><div className="loading"><div className="spinner" />验证身份中...</div></div>;
    }
    if (!loggedIn) {
        return <div className="page"><LoginPage onLogin={handleLogin} /></div>;
    }

    const currentFieldInfo = categories
        .find(c => c.name === selectedCategory)
        ?.fields?.find(f => f.name === selectedField);

    return (
        <div className="page">
            {/* Mobile header */}
            <div className="mobile-header">
                <button className="hamburger" onClick={() => setSidebarOpen(!sidebarOpen)} aria-label="菜单">
                    <span /><span /><span />
                </button>
                <h2>翻译校对</h2>
                <span className="mobile-user">{currentUser}</span>
            </div>

            <div className="layout">
                {sidebarOpen && <div className="overlay" onClick={() => setSidebarOpen(false)} />}

                {/* Sidebar */}
                <aside className={`sidebar ${sidebarOpen ? "open" : ""}`}>
                    <div className="sidebar-header">
                        <h2>翻译校对</h2>
                        <span className="sidebar-user">{currentUser}</span>
                    </div>

                    <div className="sidebar-filter">
                        <label>来源过滤</label>
                        <select value={sourceFilter} onChange={e => setSourceFilter(e.target.value)}>
                            <option value="">全部</option>
                            <option value="llm">仅 AI 翻译</option>
                            <option value="human">仅人工校对</option>
                            <option value="pinned">仅锁定</option>
                            <option value="cn">仅官方</option>
                            <option value="unknown">仅未知</option>
                        </select>
                    </div>

                    <div className="sidebar-categories">
                        {categories.map(cat => (
                            <div key={cat.name} className="category-group">
                                <div className="category-name">{CATEGORY_LABELS[cat.name] || cat.name}</div>
                                {cat.fields?.map(field => {
                                    const needsWork = field.llmCount + field.unknownCount;
                                    return (
                                        <div
                                            key={`${cat.name}-${field.name}`}
                                            className={`field-item ${selectedCategory === cat.name && selectedField === field.name ? "active" : ""}`}
                                            onClick={() => handleFieldSelect(cat.name, field.name)}
                                        >
                                            <span>{FIELD_LABELS[field.name] || field.name}</span>
                                            <div className="field-stats">
                                                {needsWork > 0 && <span className="badge llm">{needsWork}</span>}
                                                {field.humanCount > 0 && <span className="badge human">{field.humanCount}</span>}
                                                {field.pinnedCount > 0 && <span className="badge pinned">{field.pinnedCount}</span>}
                                            </div>
                                        </div>
                                    );
                                })}
                            </div>
                        ))}
                    </div>

                    <div className="sidebar-footer">
                        {/* Event Stories Section — collapsible */}
                        <details className="sidebar-stories">
                            <summary className="sidebar-stories-summary">
                                活动剧情翻译 <span className="badge llm">{eventStories.length}</span>
                            </summary>
                            {eventStories.length === 0 ? (
                                <div className="empty" style={{ padding: "0.5rem" }}>
                                    <p style={{ fontSize: "0.8rem" }}>暂无活动剧情翻译文件</p>
                                </div>
                            ) : (
                                <div className="sidebar-story-content">
                                    <select
                                        className="sidebar-story-select"
                                        value={selectedEventId ?? ""}
                                        onChange={e => setSelectedEventId(Number(e.target.value))}
                                    >
                                        {eventStories.map(story => (
                                            <option key={story.eventId} value={story.eventId}>
                                                Event #{story.eventId} · {story.source} · {story.episodeCount} 章
                                            </option>
                                        ))}
                                    </select>
                                    {storyLoading ? (
                                        <div className="loading" style={{ height: "auto", padding: "0.5rem" }}>
                                            <div className="spinner" />加载中...
                                        </div>
                                    ) : !selectedStoryDetail ? (
                                        <div className="empty" style={{ padding: "0.5rem" }}>
                                            <p style={{ fontSize: "0.8rem" }}>未找到详情</p>
                                        </div>
                                    ) : (
                                        <div className="sidebar-story-episodes">
                                            {Object.entries(selectedStoryDetail.episodes)
                                                .sort((a, b) => Number(a[0]) - Number(b[0]))
                                                .map(([episodeNo, ep]) => (
                                                    <details key={episodeNo} className="sidebar-story-episode">
                                                        <summary>
                                                            第 {episodeNo} 章 {ep.title ? `· ${ep.title}` : ""}
                                                            <span>{Object.keys(ep.talkData || {}).length} 条</span>
                                                        </summary>
                                                        <div className="sidebar-story-lines">
                                                            {Object.entries(ep.talkData || {}).map(([jp, cn]) => (
                                                                <div key={`${episodeNo}-${jp}`} className="sidebar-story-line">
                                                                    <div className="jp">{jp}</div>
                                                                    <input
                                                                        type="text"
                                                                        className="story-line-input"
                                                                        defaultValue={cn}
                                                                        onBlur={e => {
                                                                            const val = e.target.value;
                                                                            if (val !== cn && selectedEventId) {
                                                                                handleStoryLineUpdate(selectedEventId, episodeNo, jp, val);
                                                                            }
                                                                        }}
                                                                        onKeyDown={e => {
                                                                            if (e.key === "Enter") {
                                                                                (e.target as HTMLInputElement).blur();
                                                                            }
                                                                        }}
                                                                    />
                                                                </div>
                                                            ))}
                                                        </div>
                                                    </details>
                                                ))}
                                        </div>
                                    )}
                                </div>
                            )}
                        </details>

                        <button className="push-btn" onClick={handlePush} disabled={pushing}>
                            {pushing ? "备份中..." : "备份"}
                        </button>
                        <button className="sync-btn" onClick={handleCNSync} disabled={syncingCN || pushing || aiTranslating}>
                            {syncingCN ? "更新中..." : "数据更新"}
                        </button>
                        <button className="btn-ai-all" onClick={handleAITranslateAll} disabled={aiTranslating || syncingCN}>
                            {aiTranslating ? "AI翻译中..." : "🤖 一键AI补充缺失字段"}
                        </button>
                        {pushStatus?.lastPush && (
                            <div className="push-status">
                                上次推送: {new Date(pushStatus.lastPush).toLocaleString("zh-CN")}
                            </div>
                        )}
                        {pushStatus?.lastError && (
                            <div className="push-status" style={{ color: "#ef4444" }}>
                                错误: {pushStatus.lastError}
                            </div>
                        )}
                        {mounted && (
                            <div className="theme-container">
                                <span>主题模式</span>
                                <select className="theme-select" value={theme} onChange={e => setTheme(e.target.value)}>
                                    <option value="system">跟随系统</option>
                                    <option value="light">亮色</option>
                                    <option value="dark">深色</option>
                                </select>
                            </div>
                        )}
                        <div className="theme-container">
                            <span>AI提供方</span>
                            <select className="theme-select" value={aiProvider} onChange={e => setAIProvider(e.target.value as "gemini" | "openai")}>
                                <option value="gemini">Gemini</option>
                                <option value="openai">OpenAI兼容</option>
                            </select>
                        </div>
                        <button className="btn-logout" onClick={handleLogout}>退出登录</button>
                    </div>
                </aside>

                {/* Main content */}
                <main className="main">
                    {!selectedCategory || !selectedField ? (
                        <div className="empty">
                            <p>← 选择一个翻译类别</p>
                            <span>从左侧面板选择类别和字段开始校对</span>
                        </div>
                    ) : (
                        <>
                            <div className="main-header">
                                <h1>{CATEGORY_LABELS[selectedCategory] || selectedCategory} / {FIELD_LABELS[selectedField] || selectedField}</h1>
                                <span className="entry-count">
                                    {selectedIndex >= 0 ? `${selectedIndex + 1} / ` : ""}{filteredEntries.length} 条
                                    {currentFieldInfo && ` (total: ${currentFieldInfo.total})`}
                                </span>
                            </div>

                            <div className="search-bar">
                                <input type="text" placeholder="搜索日文或中文..." value={searchQuery} onChange={e => setSearchQuery(e.target.value)} />
                            </div>

                            {/* Proofreading Panel */}
                            {selectedEntry && (
                                <div className="proof-panel">
                                    <div className="proof-original">
                                        <label>日文原文</label>
                                        <div className="proof-jp">{selectedEntry.key}</div>
                                    </div>
                                    <div className="proof-edit">
                                        <div className="proof-edit-header">
                                            <label>
                                                翻译校对
                                                <span className={`source-tag ${selectedEntry.source}`} style={{ marginLeft: "0.5rem" }}>
                                                    {SOURCE_LABELS[selectedEntry.source] || selectedEntry.source}
                                                </span>
                                            </label>
                                            <div className="proof-nav">
                                                <button onClick={() => navigateEntry(-1)} disabled={selectedIndex <= 0}>↑ 上一条</button>
                                                <button onClick={() => navigateEntry(1)} disabled={selectedIndex >= filteredEntries.length - 1}>下一条 ↓</button>
                                            </div>
                                        </div>
                                        <textarea
                                            ref={editRef}
                                            className={`proof-textarea ${!isEditing ? "gray" : ""}`}
                                            value={editValue}
                                            onChange={e => { setIsEditing(true); setEditValue(e.target.value); }}
                                            onClick={() => setIsEditing(true)}
                                            onKeyDown={handleTextareaKeyDown}
                                            placeholder="输入翻译..."
                                            rows={3}
                                        />
                                        <div className="proof-actions">
                                            <button className="btn-save" onClick={() => handleSave()}>✓ 保存并下一条</button>
                                            <button className="btn-pinned" onClick={() => handleSave("pinned")}>🔒 锁定保存</button>
                                            <button className="btn-cancel" onClick={() => { setSelectedKey(null); setIsEditing(false); }}>取消</button>
                                            <div className="proof-hints">
                                                <kbd>Enter</kbd> 保存 <kbd>Ctrl+↑↓</kbd> 切换 <kbd>Esc</kbd> 取消
                                            </div>
                                        </div>
                                    </div>
                                </div>
                            )}

                            {/* Entry List */}
                            {loadingEntries ? (
                                <div className="loading"><div className="spinner" />加载中...</div>
                            ) : filteredEntries.length === 0 ? (
                                <div className="empty">
                                    <p>暂无数据</p>
                                    <span>{searchQuery ? "尝试其他搜索关键词" : "该字段下没有翻译条目"}</span>
                                </div>
                            ) : (
                                <div className="entry-list-wrapper">
                                    <table className="translation-table">
                                        <thead>
                                            <tr>
                                                <th className="col-source">来源</th>
                                                <th className="col-jp">日文原文</th>
                                                <th className="col-cn">当前翻译</th>
                                            </tr>
                                        </thead>
                                        <tbody>
                                            {filteredEntries.map(entry => (
                                                <tr
                                                    key={entry.key}
                                                    data-key={entry.key}
                                                    className={`entry-row ${selectedKey === entry.key ? "row-active" : ""}`}
                                                    onClick={() => selectEntry(entry.key)}
                                                >
                                                    <td onClick={e => e.stopPropagation()}>
                                                        <select
                                                            value={entry.source}
                                                            onChange={e => handleSourceChange(entry.key, e.target.value)}
                                                            className={`source-tag ${entry.source}`}
                                                        >
                                                            {Object.entries(SOURCE_LABELS).map(([k, v]) => (
                                                                <option key={k} value={k}>{v}</option>
                                                            ))}
                                                        </select>
                                                    </td>
                                                    <td><div className="jp-text">{entry.key}</div></td>
                                                    <td><div className="cn-text">{entry.text}</div></td>
                                                </tr>
                                            ))}
                                        </tbody>
                                    </table>
                                </div>
                            )}
                        </>
                    )}
                </main>
            </div>

            {/* Toasts */}
            {toasts.map(t => (
                <div key={t.id} className={`toast ${t.type}`}>{t.msg}</div>
            ))}
        </div>
    );
}
