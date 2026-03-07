"use client";

import React, { useState, useEffect, useCallback, useRef, useMemo } from "react";
import {
    getToken, setToken, clearToken, getUsername, setUsername,
    login, getCategories, getEntries, updateEntry, pushToHub, getPushStatus,
    triggerAITranslateAll, getEventStories, getEventStory, runCNSync, getTranslateStatus,
    updateEventStoryLine,
    type CategoryInfo, type TranslationEntry, type PushStatus,
    type EventStorySummary, type EventStoryDetail, type TranslateStatusResponse,
} from "@/lib/api";
import { useTheme } from "next-themes";

// ============================================================================
// Labels
// ============================================================================

const CATEGORY_LABELS: Record<string, string> = {
    cards: "卡牌", events: "活动", music: "音乐", gacha: "卡池",
    virtualLive: "虚拟Live", sticker: "贴纸", comic: "漫画",
    mysekai: "我的世界", costumes: "服装", characters: "角色", units: "团体",
    eventStory: "活动剧情",
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
    const syncingCNRef = useRef(false);

    // AI translation
    const [aiProvider, setAIProvider] = useState<"gemini" | "openai">("gemini");
    const [aiTranslating, setAITranslating] = useState(false);
    const aiTranslatingRef = useRef(false);
    const [translateStatus, setTranslateStatus] = useState<TranslateStatusResponse | null>(null);

    // Event story block
    const [eventStories, setEventStories] = useState<EventStorySummary[]>([]);

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
    const backendTranslatorRunning = Boolean(translateStatus?.translator?.running);
    const backendSchedulerRunning = Boolean(translateStatus?.scheduler?.running);

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

        if (selectedCategory === "eventStory") {
            const eventId = Number(selectedField);
            getEventStory(eventId)
                .then(detail => {
                    const newEntries: TranslationEntry[] = [];
                    Object.entries(detail.episodes)
                        .sort((a, b) => Number(a[0]) - Number(b[0]))
                        .forEach(([episodeNo, ep]) => {
                            Object.entries(ep.talkData || {}).forEach(([jp, cn]) => {
                                newEntries.push({
                                    key: `${episodeNo}|${jp}`,
                                    text: cn,
                                    source: "human" // Event stories use human translation for now
                                });
                            });
                        });
                    setEntries(newEntries);
                    if (newEntries.length > 0) {
                        setSelectedKey(newEntries[0].key);
                        setEditValue(newEntries[0].text);
                        setIsEditing(false);
                    }
                })
                .catch(err => showToast(err.message, "err"))
                .finally(() => setLoadingEntries(false));
            return;
        }

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
            })
            .catch(() => {
                setEventStories([]);
            });
    }, [loggedIn]);

    useEffect(() => {
        if (!loggedIn) return;
        const fetchStatus = () => getTranslateStatus().then(setTranslateStatus).catch(() => { });
        fetchStatus();
        const iv = setInterval(fetchStatus, 5000);
        return () => clearInterval(iv);
    }, [loggedIn]);



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
            if (selectedCategory === "eventStory") {
                const parts = selectedKey.split("|");
                const episodeNo = parts[0];
                const jp = parts.slice(1).join("|");
                await updateEventStoryLine(Number(selectedField), episodeNo, jp, editValue);

                setEntries(prev => prev.map(e =>
                    e.key === selectedKey ? { ...e, text: editValue, source: src } : e
                ));
                showToast("剧情翻译已保存", "ok");
            } else {
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
        if (!selectedCategory || !selectedField || selectedCategory === "eventStory") return;
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
        if (syncingCNRef.current || backendSchedulerRunning || backendTranslatorRunning) {
            showToast("已有翻译任务在运行，请稍后再试", "err");
            return;
        }
        syncingCNRef.current = true;
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
            const message = err instanceof Error ? err.message : "数据更新失败";
            if (message.includes("already running")) {
                showToast("已有同步任务在运行，请稍后查看状态", "err");
            } else {
                showToast(message, "err");
            }
            getTranslateStatus().then(setTranslateStatus).catch(() => { });
        } finally {
            syncingCNRef.current = false;
            setSyncingCN(false);
        }
    };

    const handleAITranslateAll = async () => {
        if (aiTranslatingRef.current || backendTranslatorRunning || backendSchedulerRunning) {
            showToast("已有翻译任务在运行，请稍后再试", "err");
            return;
        }
        aiTranslatingRef.current = true;
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
            const message = err instanceof Error ? err.message : "AI翻译失败";
            if (message.includes("already running")) {
                showToast("已有翻译任务在运行，请稍后再试", "err");
            } else {
                showToast(message, "err");
            }
            getTranslateStatus().then(setTranslateStatus).catch(() => { });
        } finally {
            aiTranslatingRef.current = false;
            setAITranslating(false);
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
                        {eventStories.length > 0 && (
                            <details className="category-group" open={selectedCategory === "eventStory"}>
                                <summary className="category-name" style={{ cursor: "pointer", display: "flex", justifyContent: "space-between", alignItems: "center" }}>
                                    <span>活动剧情翻译 <span className="badge llm" style={{ marginLeft: "4px" }}>{eventStories.length}</span></span>
                                </summary>
                                <div style={{ maxHeight: "30vh", overflowY: "auto", marginTop: "0.5rem", borderTop: "1px solid var(--border)", paddingTop: "0.5rem" }}>
                                    {eventStories.map(story => (
                                        <div
                                            key={`eventStory-${story.eventId}`}
                                            className={`field-item ${selectedCategory === "eventStory" && selectedField === String(story.eventId) ? "active" : ""}`}
                                            onClick={() => handleFieldSelect("eventStory", String(story.eventId))}
                                        >
                                            <span>Event #{story.eventId}</span>
                                            <div className="field-stats">
                                                <span className="badge cn">{story.episodeCount}章</span>
                                            </div>
                                        </div>
                                    ))}
                                </div>
                            </details>
                        )}

                        <button className="push-btn" onClick={handlePush} disabled={pushing}>
                            {pushing ? "备份中..." : "备份"}
                        </button>
                        <button className="sync-btn" onClick={handleCNSync} disabled={syncingCN || pushing || aiTranslating || backendSchedulerRunning || backendTranslatorRunning}>
                            {(syncingCN || backendSchedulerRunning) ? "更新中..." : "数据更新"}
                        </button>
                        <button className="btn-ai-all" onClick={handleAITranslateAll} disabled={aiTranslating || syncingCN || backendTranslatorRunning || backendSchedulerRunning}>
                            {(aiTranslating || backendTranslatorRunning) ? "AI翻译中..." : "🤖 一键AI补充缺失字段"}
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
                                <h1>{CATEGORY_LABELS[selectedCategory] || selectedCategory} / {selectedCategory === "eventStory" ? `Event #${selectedField}` : (FIELD_LABELS[selectedField] || selectedField)}</h1>
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
                                        <div className="proof-jp">
                                            {selectedCategory === "eventStory" ? selectedEntry.key.split("|").slice(1).join("|") : selectedEntry.key}
                                        </div>
                                        {selectedCategory === "eventStory" && (
                                            <div style={{ fontSize: "0.85em", color: "var(--text-secondary)", marginTop: "4px" }}>
                                                [第 {selectedEntry.key.split("|")[0]} 章]
                                            </div>
                                        )}
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
                                                    <td><div className="jp-text">
                                                        {selectedCategory === "eventStory" ? entry.key.split("|").slice(1).join("|") : entry.key}
                                                        {selectedCategory === "eventStory" && (
                                                            <div style={{ fontSize: "0.75em", color: "var(--text-secondary)", marginTop: "4px" }}>
                                                                第 {entry.key.split("|")[0]} 章
                                                            </div>
                                                        )}
                                                    </div></td>
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
