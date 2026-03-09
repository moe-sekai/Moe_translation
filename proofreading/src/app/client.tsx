"use client";

import React, { useState, useEffect, useCallback, useRef, useMemo } from "react";
import {
    getToken, setToken, clearToken, getUsername, setUsername,
    login, getCategories, getEntries, updateEntry, pushToHub, pullLatestBackup, getPushStatus,
    triggerAITranslateAll, getEventStories, getEventStory, runCNSync, getTranslateStatus,
    updateEventStoryLine, promoteEventStoryHuman,
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

const SOURCE_BASE = (process.env.NEXT_PUBLIC_PJSK_BASE || "https://pjsk.moe").replace(/\/+$/, "");
const EVENT_STORY_TITLE_MARKER = "__title__";

function normalizeEventStorySource(source: string | undefined): string {
    switch ((source || "").trim().toLowerCase()) {
        case "official_cn":
        case "official_cn_legacy":
        case "cn":
            return "cn";
        case "llm":
            return "llm";
        case "human":
        case "pinned":
        case "unknown":
            return (source || "unknown").trim().toLowerCase();
        case "jp_pending":
        default:
            return "unknown";
    }
}

function buildEventStoryEntries(detail: EventStoryDetail): TranslationEntry[] {
    const storySource = normalizeEventStorySource(detail.meta?.source);
    const entries: TranslationEntry[] = [];
    Object.entries(detail.episodes)
        .sort((a, b) => Number(a[0]) - Number(b[0]))
        .forEach(([episodeNo, ep]) => {
            if ((ep.title || "").trim() !== "") {
                entries.push({
                    key: `${episodeNo}|${EVENT_STORY_TITLE_MARKER}|${ep.title}`,
                    text: ep.title,
                    source: ep.titleSource || storySource,
                });
            }
            Object.entries(ep.talkData || {}).forEach(([jp, cn]) => {
                entries.push({
                    key: `${episodeNo}|${jp}`,
                    text: cn,
                    source: ep.talkSources?.[jp] || storySource,
                });
            });
        });
    return entries;
}

function parseEventStoryEntryKey(key: string): { episodeNo: string; entryType: "title" | "talk"; originalText: string } {
    const parts = key.split("|");
    const episodeNo = parts[0] || "";
    if (parts[1] === EVENT_STORY_TITLE_MARKER) {
        return {
            episodeNo,
            entryType: "title",
            originalText: parts.slice(2).join("|") || "[章节标题]",
        };
    }
    return {
        episodeNo,
        entryType: "talk",
        originalText: parts.slice(1).join("|"),
    };
}

function getEventStoryEntryLabel(key: string): string {
    const parsed = parseEventStoryEntryKey(key);
    return parsed.entryType === "title" ? `[章节标题] ${parsed.originalText}` : parsed.originalText;
}

function getEventStoryEntrySearchText(entry: TranslationEntry): string {
    return `${getEventStoryEntryLabel(entry.key)}\n${entry.text}`.toLowerCase();
}

const DETAIL_BUILDERS: Record<string, (id: string) => string> = {
    cards: (id) => `${SOURCE_BASE}/cards/${id}/`,
    events: (id) => `${SOURCE_BASE}/events/${id}/`,
    gacha: (id) => `${SOURCE_BASE}/gacha/${id}/`,
    virtualLive: (id) => `${SOURCE_BASE}/live/${id}/`,
    music: (id) => `${SOURCE_BASE}/music/${id}/`,
    mysekai: (id) => `${SOURCE_BASE}/mysekai/${id}/`,
    costumes: (id) => `${SOURCE_BASE}/costumes/${id}/`,
    characters: (id) => `${SOURCE_BASE}/character/${id}/`,
};

type DraftRecord = Record<string, { text: string; updatedAt: number }>;

function makeDraftStorageKey(username: string, category: string, field: string): string {
    return `translate-drafts:${username || "anonymous"}:${category}:${field}`;
}

function loadDrafts(storageKey: string): DraftRecord {
    if (typeof window === "undefined") return {};
    try {
        const raw = localStorage.getItem(storageKey);
        if (!raw) return {};
        const parsed = JSON.parse(raw) as DraftRecord;
        if (!parsed || typeof parsed !== "object") return {};
        return parsed;
    } catch {
        return {};
    }
}

function saveDrafts(storageKey: string, drafts: DraftRecord) {
    if (typeof window === "undefined") return;
    if (Object.keys(drafts).length === 0) {
        localStorage.removeItem(storageKey);
        return;
    }
    localStorage.setItem(storageKey, JSON.stringify(drafts));
}

function mergeEntriesWithDrafts(entries: TranslationEntry[], drafts: DraftRecord): TranslationEntry[] {
    if (Object.keys(drafts).length === 0) return entries;
    return entries.map(entry => {
        const draft = drafts[entry.key];
        if (!draft) return entry;
        return { ...entry, text: draft.text };
    });
}

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
    const [drafts, setDrafts] = useState<DraftRecord>({});
    const [rowDetailMenuKey, setRowDetailMenuKey] = useState<string | null>(null);
    const [rowLastDetailId, setRowLastDetailId] = useState<Record<string, string>>({});
    const [detailMenuOpen, setDetailMenuOpen] = useState(false);
    const [lastDetailId, setLastDetailId] = useState("");

    // Edit
    const [editValue, setEditValue] = useState("");
    const [isEditing, setIsEditing] = useState(false);
    const editRef = useRef<HTMLTextAreaElement>(null);
    const savingRef = useRef(false);
    const autosaveTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

    // Push
    const [pushing, setPushing] = useState(false);
    const [pullingBackup, setPullingBackup] = useState(false);
    const [pushStatus, setPushStatus] = useState<PushStatus | null>(null);
    const [syncingCN, setSyncingCN] = useState(false);
    const syncingCNRef = useRef(false);

    // AI translation
    const [aiProvider, setAIProvider] = useState<"gemini" | "openai">("gemini");
    const [aiTranslating, setAITranslating] = useState(false);
    const aiTranslatingRef = useRef(false);
    const [translateStatus, setTranslateStatus] = useState<TranslateStatusResponse | null>(null);

    // Shortcut
    const [saveShortcut, setSaveShortcut] = useState<"enter" | "shift-enter">("shift-enter");

    // Ignored stats
    const [ignoredStats, setIgnoredStats] = useState<string[]>([]);

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
        return entries.filter(e =>
            selectedCategory === "eventStory"
                ? getEventStoryEntrySearchText(e).includes(q)
                : e.key.toLowerCase().includes(q) || e.text.toLowerCase().includes(q)
        );
    }, [entries, searchQuery, selectedCategory]);

    const selectedEntry = useMemo(
        () => filteredEntries.find(e => e.key === selectedKey) ?? null,
        [selectedKey, filteredEntries]
    );

    const selectedIndex = useMemo(
        () => (selectedKey ? filteredEntries.findIndex(e => e.key === selectedKey) : -1),
        [selectedKey, filteredEntries]
    );
    const draftStorageKey = useMemo(() => {
        if (!selectedCategory || !selectedField) return "";
        return makeDraftStorageKey(currentUser, selectedCategory, selectedField);
    }, [currentUser, selectedCategory, selectedField]);
    const backendTranslatorRunning = Boolean(translateStatus?.translator?.running);
    const backendSchedulerRunning = Boolean(translateStatus?.scheduler?.running);

    const detailInfo = useMemo(() => {
        if (!selectedEntry) {
            return { mode: "none" as const, label: "页面", url: "", ids: [], disabledReason: "无可用条目" };
        }
        if (selectedCategory === "eventStory") {
            const eventId = selectedField;
            if (!eventId) {
                return { mode: "none" as const, label: "页面", url: "", ids: [], disabledReason: "缺少活动ID" };
            }
            const label = `${eventId} Moesekai 页面`;
            return { mode: "single" as const, label, url: `${SOURCE_BASE}/eventstory/${eventId}/`, ids: [], disabledReason: "" };
        }
        const builder = DETAIL_BUILDERS[selectedCategory];
        if (!builder) {
            return { mode: "none" as const, label: "页面", url: "", ids: [], disabledReason: "该分类暂无来源链接" };
        }
        const ids = selectedEntry.ids || [];
        if (ids.length === 0) {
            return { mode: "none" as const, label: "页面", url: "", ids, disabledReason: "缺少来源ID" };
        }
        if (ids.length === 1) {
            return { mode: "single" as const, label: `${ids[0]} Moesekai 页面`, url: builder(ids[0]), ids, disabledReason: "" };
        }
        const label = lastDetailId && ids.includes(lastDetailId)
            ? `Moesekai 页面 (${lastDetailId})`
            : `Moesekai 页面 (${ids.length})`;
        return { mode: "multi" as const, label, ids, builder, disabledReason: "" };
    }, [selectedEntry, selectedCategory, selectedField, lastDetailId]);

    const handleOpenDetail = useCallback((url: string) => {
        if (!url) return;
        window.open(url, "_blank", "noopener,noreferrer");
    }, []);

    const detailMenuRef = useRef<HTMLDivElement>(null);

    useEffect(() => {
        setDetailMenuOpen(false);
        setLastDetailId("");
    }, [selectedEntry?.key, selectedCategory, selectedField]);

    useEffect(() => {
        if (!detailMenuOpen) return;
        const handleClick = (e: MouseEvent) => {
            if (!detailMenuRef.current) return;
            if (detailMenuRef.current.contains(e.target as Node)) return;
            setDetailMenuOpen(false);
        };
        window.addEventListener("click", handleClick);
        return () => window.removeEventListener("click", handleClick);
    }, [detailMenuOpen]);

    useEffect(() => {
        if (!rowDetailMenuKey) return;
        const handleClick = (e: MouseEvent) => {
            const target = e.target as HTMLElement | null;
            if (target && target.closest(".detail-menu")) return;
            setRowDetailMenuKey(null);
        };
        window.addEventListener("click", handleClick);
        return () => window.removeEventListener("click", handleClick);
    }, [rowDetailMenuKey]);

    const buildRowDetail = useCallback((entry: TranslationEntry) => {
        if (selectedCategory === "eventStory") {
            if (!selectedField) {
                return { mode: "none" as const, label: "页面", url: "", disabledReason: "缺少活动ID" };
            }
            return {
                mode: "single" as const,
                label: `${selectedField} 页面`,
                url: `${SOURCE_BASE}/eventstory/${selectedField}/`,
            };
        }
        const builder = DETAIL_BUILDERS[selectedCategory];
        if (!builder) {
            return { mode: "none" as const, label: "页面", url: "", disabledReason: "该分类暂无来源链接" };
        }
        const ids = entry.ids || [];
        if (ids.length === 0) {
            return { mode: "none" as const, label: "页面", url: "", disabledReason: "缺少来源ID" };
        }
        if (ids.length === 1) {
            return { mode: "single" as const, label: `${ids[0]} 页面`, url: builder(ids[0]) };
        }
        const last = rowLastDetailId[entry.key];
        const label = last && ids.includes(last) ? `页面 (${last})` : `页面 (${ids.length})`;
        return { mode: "multi" as const, label, ids, builder };
    }, [selectedCategory, selectedField, rowLastDetailId]);

    // ---- Auth check on mount ----
    useEffect(() => {
        setMounted(true);
        const storedShortcut = localStorage.getItem("save-shortcut");
        if (storedShortcut === "enter" || storedShortcut === "shift-enter") {
            setSaveShortcut(storedShortcut);
        }

        const storedIgnored = localStorage.getItem("ignored-stats");
        if (storedIgnored) {
            try {
                setIgnoredStats(JSON.parse(storedIgnored));
            } catch (e) { }
        }

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

        const storageKey = makeDraftStorageKey(currentUser, selectedCategory, selectedField);
        const loadedDrafts = loadDrafts(storageKey);
        setDrafts(loadedDrafts);

        if (selectedCategory === "eventStory") {
            const eventId = Number(selectedField);
            getEventStory(eventId)
                .then(detail => {
                    const newEntries = buildEventStoryEntries(detail);
                    const mergedEntries = mergeEntriesWithDrafts(newEntries, loadedDrafts);
                    setEntries(mergedEntries);
                    if (mergedEntries.length > 0) {
                        setSelectedKey(mergedEntries[0].key);
                        setEditValue(mergedEntries[0].text);
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
                data.sort((a, b) => {
                    const diff = (order[a.source] ?? 5) - (order[b.source] ?? 5);
                    if (diff !== 0) return diff;
                    return a.key.localeCompare(b.key, undefined, { numeric: true });
                });
                const mergedEntries = mergeEntriesWithDrafts(data, loadedDrafts);
                setEntries(mergedEntries);
                if (mergedEntries.length > 0) {
                    setSelectedKey(mergedEntries[0].key);
                    setEditValue(mergedEntries[0].text);
                    setIsEditing(false);
                }
            })
            .catch(err => showToast(err.message, "err"))
            .finally(() => setLoadingEntries(false));
    }, [selectedCategory, selectedField, sourceFilter, loggedIn, showToast, currentUser]);

    useEffect(() => {
        if (!draftStorageKey) return;
        saveDrafts(draftStorageKey, drafts);
    }, [draftStorageKey, drafts]);

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
        if (entry) setEditValue(drafts[key]?.text ?? entry.text);
    }, [entries, drafts]);

    const navigateEntry = useCallback((dir: 1 | -1) => {
        if (selectedIndex < 0) return;
        const idx = selectedIndex + dir;
        if (idx < 0 || idx >= filteredEntries.length) return;
        const next = filteredEntries[idx];
        setSelectedKey(next.key);
        setEditValue(drafts[next.key]?.text ?? next.text);
        setIsEditing(false);
        document.querySelector(`[data-key="${CSS.escape(next.key)}"]`)
            ?.scrollIntoView({ block: "center", behavior: "smooth" });
    }, [selectedIndex, filteredEntries, drafts]);

    const saveDraft = useCallback((key: string, text: string) => {
        setDrafts(prev => ({
            ...prev,
            [key]: { text, updatedAt: Date.now() },
        }));
    }, []);

    const clearDraft = useCallback((key: string) => {
        setDrafts(prev => {
            if (!prev[key]) return prev;
            const next = { ...prev };
            delete next[key];
            return next;
        });
    }, []);

    const autoSaveCurrent = useCallback(async () => {
        if (savingRef.current || !selectedKey || !selectedCategory || !selectedField) return;
        const current = entries.find(e => e.key === selectedKey);
        if (!current) return;
        const hasDraft = Boolean(drafts[selectedKey]);
        if (editValue === current.text && !hasDraft) {
            clearDraft(selectedKey);
            return;
        }

        savingRef.current = true;
        try {
            if (selectedCategory === "eventStory") {
                const parsed = parseEventStoryEntryKey(selectedKey);
                await updateEventStoryLine(Number(selectedField), parsed.episodeNo, parsed.entryType === "title" ? "" : parsed.originalText, editValue, "human", parsed.entryType);
            } else {
                await updateEntry(selectedCategory, selectedField, selectedKey, editValue, "human");
            }
            setEntries(prev => prev.map(e => {
                if (e.key !== selectedKey) return e;
                const parsed = selectedCategory === "eventStory" ? parseEventStoryEntryKey(e.key) : null;
                return {
                    ...e,
                    key: parsed?.entryType === "title" ? `${parsed.episodeNo}|${EVENT_STORY_TITLE_MARKER}|${editValue}` : e.key,
                    text: editValue,
                    source: "human",
                };
            }));
            if (selectedCategory === "eventStory") {
                const parsed = parseEventStoryEntryKey(selectedKey);
                if (parsed.entryType === "title") {
                    setSelectedKey(`${parsed.episodeNo}|${EVENT_STORY_TITLE_MARKER}|${editValue}`);
                }
            }
            clearDraft(selectedKey);
        } catch {
            showToast("自动保存失败，内容已本地暂存", "err");
        } finally {
            savingRef.current = false;
        }
    }, [selectedKey, selectedCategory, selectedField, editValue, entries, drafts, clearDraft, showToast]);

    useEffect(() => {
        if (!selectedKey || !isEditing) return;
        if (autosaveTimerRef.current) {
            clearTimeout(autosaveTimerRef.current);
        }
        autosaveTimerRef.current = setTimeout(() => {
            void autoSaveCurrent();
        }, 1200);
        return () => {
            if (autosaveTimerRef.current) {
                clearTimeout(autosaveTimerRef.current);
            }
        };
    }, [selectedKey, isEditing, editValue, autoSaveCurrent]);

    const handleSave = useCallback(async (overrideSource?: string) => {
        if (savingRef.current || !selectedKey || !selectedCategory || !selectedField) return;
        savingRef.current = true;
        const src = overrideSource || "human";

        try {
            if (selectedCategory === "eventStory") {
                const parsed = parseEventStoryEntryKey(selectedKey);
                await updateEventStoryLine(Number(selectedField), parsed.episodeNo, parsed.entryType === "title" ? "" : parsed.originalText, editValue, src, parsed.entryType);

                setEntries(prev => prev.map(e =>
                    e.key === selectedKey
                        ? {
                            ...e,
                            key: parsed.entryType === "title" ? `${parsed.episodeNo}|${EVENT_STORY_TITLE_MARKER}|${editValue}` : e.key,
                            text: editValue,
                            source: src,
                        }
                        : e
                ));
                if (parsed.entryType === "title") {
                    setSelectedKey(`${parsed.episodeNo}|${EVENT_STORY_TITLE_MARKER}|${editValue}`);
                }
                clearDraft(selectedKey);
                showToast("剧情翻译已保存", "ok");
            } else {
                const result = await updateEntry(selectedCategory, selectedField, selectedKey, editValue, src);

                // Update local state
                setEntries(prev => prev.map(e =>
                    e.key === selectedKey ? { ...e, text: editValue, source: src } : e
                ));
                clearDraft(selectedKey);

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
                setTimeout(() => {
                    document.querySelector(`[data-key="${CSS.escape(next.key)}"]`)
                        ?.scrollIntoView({ block: "center", behavior: "smooth" });
                }, 50);
            } else {
                showToast("已到最后一条", "ok");
            }
        } catch (err) {
            showToast(err instanceof Error ? err.message : "保存失败", "err");
        } finally {
            savingRef.current = false;
        }
    }, [selectedKey, selectedCategory, selectedField, editValue, filteredEntries, showToast, clearDraft, entries]);

    const handlePromoteCurrentEventStoryHuman = useCallback(async () => {
	    if (savingRef.current || selectedCategory !== "eventStory" || !selectedField) return;
	    await autoSaveCurrent();
	    savingRef.current = true;
	    try {
	        await promoteEventStoryHuman(Number(selectedField));
	        setEntries(prev => prev.map(entry => ({ ...entry, source: "human" })));
	        const stories = await getEventStories();
	        setEventStories(stories);
	        showToast("已将当前剧情整篇标记为人工", "ok");
	    } catch (err) {
	        showToast(err instanceof Error ? err.message : "整篇标记失败", "err");
	    } finally {
	        savingRef.current = false;
	    }
    }, [autoSaveCurrent, selectedCategory, selectedField, showToast]);

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
            showToast("上传本地数据成功", "ok");
            getPushStatus().then(setPushStatus);
        } catch (err) {
            showToast(err instanceof Error ? err.message : "上传失败", "err");
        } finally {
            setPushing(false);
        }
    };

    const handlePullLatestBackup = async () => {
        setPullingBackup(true);
        try {
            await pullLatestBackup();
            showToast("已拉取 backup-translations 最新备份", "ok");

            const cats = await getCategories();
            setCategories(cats);

            if (selectedCategory && selectedField) {
                if (selectedCategory === "eventStory") {
                    const detail = await getEventStory(Number(selectedField));
                    const newEntries = buildEventStoryEntries(detail);
                    const merged = mergeEntriesWithDrafts(newEntries, drafts);
                    setEntries(merged);
                    if (selectedKey) {
                        const current = merged.find(e => e.key === selectedKey);
                        if (current) {
                            setEditValue(drafts[selectedKey]?.text ?? current.text);
                        }
                    }
                } else {
                    const data = await getEntries(selectedCategory, selectedField, sourceFilter || undefined);
                    const order: Record<string, number> = { unknown: 0, llm: 1, human: 2, pinned: 3, cn: 4 };
                    data.sort((a, b) => {
                        const diff = (order[a.source] ?? 5) - (order[b.source] ?? 5);
                        if (diff !== 0) return diff;
                        return a.key.localeCompare(b.key, undefined, { numeric: true });
                    });
                    const merged = mergeEntriesWithDrafts(data, drafts);
                    setEntries(merged);
                    if (selectedKey) {
                        const current = merged.find(e => e.key === selectedKey);
                        if (current) {
                            setEditValue(drafts[selectedKey]?.text ?? current.text);
                        }
                    }
                }
            }

            const stories = await getEventStories();
            setEventStories(stories);
        } catch (err) {
            showToast(err instanceof Error ? err.message : "拉取备份失败", "err");
        } finally {
            setPullingBackup(false);
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
                if (selectedCategory === "eventStory") {
                    const detail = await getEventStory(Number(selectedField));
                    setEntries(mergeEntriesWithDrafts(buildEventStoryEntries(detail), drafts));
                } else {
                    const data = await getEntries(selectedCategory, selectedField, sourceFilter || undefined);
                    const order: Record<string, number> = { unknown: 0, llm: 1, human: 2, pinned: 3, cn: 4 };
                    data.sort((a, b) => {
                        const diff = (order[a.source] ?? 5) - (order[b.source] ?? 5);
                        if (diff !== 0) return diff;
                        return a.key.localeCompare(b.key, undefined, { numeric: true });
                    });
                    setEntries(mergeEntriesWithDrafts(data, drafts));
                }
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
                data.sort((a, b) => {
                    const diff = (order[a.source] ?? 5) - (order[b.source] ?? 5);
                    if (diff !== 0) return diff;
                    return a.key.localeCompare(b.key, undefined, { numeric: true });
                });
                setEntries(mergeEntriesWithDrafts(data, drafts));
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

    const checkModifier = useCallback((e: React.KeyboardEvent | KeyboardEvent) => {
        const isMac = typeof window !== "undefined" && navigator.userAgent.toUpperCase().indexOf("MAC") >= 0;
        return isMac ? e.metaKey && !e.ctrlKey : e.ctrlKey && !e.metaKey;
    }, []);

    const handleTextareaKeyDown = useCallback((e: React.KeyboardEvent<HTMLTextAreaElement>) => {
        const isSaveAction = saveShortcut === "shift-enter"
            ? (e.key === "Enter" && e.shiftKey)
            : (e.key === "Enter" && !e.shiftKey);

        if (isSaveAction) { e.preventDefault(); handleSave(); return; }
        if (e.key === "Escape") { e.preventDefault(); setSelectedKey(null); setIsEditing(false); }
        if (checkModifier(e) && e.key === "ArrowUp") { e.preventDefault(); navigateEntry(-1); }
        if (checkModifier(e) && e.key === "ArrowDown") { e.preventDefault(); navigateEntry(1); }
    }, [handleSave, navigateEntry, checkModifier, saveShortcut]);

    useEffect(() => {
        const handler = (e: KeyboardEvent) => {
            const tag = (e.target as HTMLElement).tagName;
            if (tag === "INPUT" || tag === "SELECT" || tag === "TEXTAREA") return;
            if (checkModifier(e) && e.key === "s") { e.preventDefault(); handleSave(); }
            if (e.key === "ArrowDown" || e.key === "j") { e.preventDefault(); navigateEntry(1); }
            if (e.key === "ArrowUp" || e.key === "k") { e.preventDefault(); navigateEntry(-1); }
            if (e.key === "Enter" && selectedKey) {
                if (document.activeElement !== editRef.current) {
                    e.preventDefault(); editRef.current?.focus();
                }
            }
            if (e.key === "Escape") { setSelectedKey(null); setIsEditing(false); }
        };
        window.addEventListener("keydown", handler);
        return () => window.removeEventListener("keydown", handler);
    }, [selectedKey, handleSave, navigateEntry, checkModifier]);

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
                                    const fieldKey = `${cat.name}:${field.name}`;
                                    const isIgnored = ignoredStats.includes(fieldKey);
                                    return (
                                        <div
                                            key={`${cat.name}-${field.name}`}
                                            className={`field-item ${selectedCategory === cat.name && selectedField === field.name ? "active" : ""}`}
                                            onClick={() => handleFieldSelect(cat.name, field.name)}
                                            onContextMenu={(e) => {
                                                e.preventDefault();
                                                const next = isIgnored ? ignoredStats.filter(k => k !== fieldKey) : [...ignoredStats, fieldKey];
                                                setIgnoredStats(next);
                                                localStorage.setItem("ignored-stats", JSON.stringify(next));
                                            }}
                                            title="右键点击可切换忽略该项的数字提示"
                                        >
                                            <span style={{ textDecoration: isIgnored ? "line-through" : "none", color: isIgnored ? "var(--text-dim)" : "inherit" }}>
                                                {FIELD_LABELS[field.name] || field.name}
                                            </span>
                                            <div className="field-stats">
                                                {!isIgnored && needsWork > 0 && <span className="badge llm">{needsWork}</span>}
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
                                    {eventStories.map(story => {
                                        const eventKey = `eventStory:${story.eventId}`;
                                        const isIgnored = ignoredStats.includes(eventKey);
                                        return (
                                            <div
                                                key={`eventStory-${story.eventId}`}
                                                className={`field-item ${selectedCategory === "eventStory" && selectedField === String(story.eventId) ? "active" : ""}`}
                                                onClick={() => handleFieldSelect("eventStory", String(story.eventId))}
                                                onContextMenu={(e) => {
                                                    e.preventDefault();
                                                    const next = isIgnored ? ignoredStats.filter(k => k !== eventKey) : [...ignoredStats, eventKey];
                                                    setIgnoredStats(next);
                                                    localStorage.setItem("ignored-stats", JSON.stringify(next));
                                                }}
                                                title="右键点击可切换忽略该项的数字提示"
                                            >
                                                <span style={{ textDecoration: isIgnored ? "line-through" : "none", color: isIgnored ? "var(--text-dim)" : "inherit" }}>
                                                    Event #{story.eventId}
                                                </span>
                                                <div className="field-stats">
                                                    {!isIgnored && story.source === "llm" && <span className="badge llm">AI</span>}
                                                    {!isIgnored && <span className="badge cn">{story.episodeCount}章</span>}
                                                </div>
                                            </div>
                                        )
                                    })}
                                </div>
                            </details>
                        )}

                        <button className="push-btn" onClick={handlePush} disabled={pushing || pullingBackup || syncingCN || aiTranslating}>
                            {pushing ? "上传中..." : "上传本地数据"}
                        </button>
                        <button className="sync-btn" onClick={handlePullLatestBackup} disabled={pullingBackup || pushing || syncingCN || aiTranslating || backendSchedulerRunning || backendTranslatorRunning}>
                            {pullingBackup ? "拉取中..." : "拉取最新备份"}
                        </button>
                        <button className="sync-btn" onClick={handleCNSync} disabled={syncingCN || pullingBackup || pushing || aiTranslating || backendSchedulerRunning || backendTranslatorRunning}>
                            {(syncingCN || backendSchedulerRunning) ? "更新中..." : "数据更新"}
                        </button>

                        <details className="category-group" style={{
                            marginTop: '0.2rem',
                            borderTop: '1px solid var(--border)',
                            paddingTop: '0.5rem',
                            marginBottom: '0'
                        }}>
                            <summary className="category-name" style={{ cursor: "pointer", display: "flex", justifyContent: "space-between", alignItems: "center" }}>
                                <span>AI 翻译设置</span>
                                <span style={{ fontSize: "10px", transform: "scale(0.8)" }}>▼</span>
                            </summary>
                            <div style={{ display: 'flex', flexDirection: 'column', gap: '0.5rem', marginTop: '0.4rem' }}>
                                <button className="btn-ai-all" onClick={handleAITranslateAll} disabled={aiTranslating || syncingCN || pullingBackup || backendTranslatorRunning || backendSchedulerRunning}>
                                    {(aiTranslating || backendTranslatorRunning) ? "AI翻译中..." : "🤖 一键AI补充缺失字段"}
                                </button>
                                <div className="theme-container" style={{ margin: 0 }}>
                                    <span>AI提供方</span>
                                    <select className="theme-select" value={aiProvider} onChange={e => setAIProvider(e.target.value as "gemini" | "openai")}>
                                        <option value="gemini">Gemini</option>
                                        <option value="openai">OpenAI兼容</option>
                                    </select>
                                </div>
                            </div>
                        </details>

                        {pushStatus?.lastPush && (
                            <div className="push-status" style={{ marginTop: '0.5rem' }}>
                                上次推送: {new Date(pushStatus.lastPush).toLocaleString("zh-CN")}
                            </div>
                        )}
                        {pushStatus?.lastError && (
                            <div className="push-status" style={{ color: "#ef4444", marginTop: '0.2rem' }}>
                                错误: {pushStatus.lastError}
                            </div>
                        )}
                        {mounted && (
                            <div className="theme-container" style={{ marginTop: '0.3rem' }}>
                                <span>主题模式</span>
                                <select className="theme-select" value={theme} onChange={e => setTheme(e.target.value)}>
                                    <option value="system">跟随系统</option>
                                    <option value="light">亮色</option>
                                    <option value="dark">深色</option>
                                </select>
                            </div>
                        )}
                        <button className="btn-logout" onClick={handleLogout} style={{ marginTop: '0.3rem' }}>退出登录</button>
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
                                            {selectedCategory === "eventStory" ? getEventStoryEntryLabel(selectedEntry.key) : selectedEntry.key}
                                            </div>
                                            {selectedCategory === "eventStory" && (
                                                <div style={{ fontSize: "0.85em", color: "var(--text-secondary)", marginTop: "4px" }}>
                                                [第 {parseEventStoryEntryKey(selectedEntry.key).episodeNo} 章]
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
                                            onChange={e => {
                                                const value = e.target.value;
                                                setIsEditing(true);
                                                setEditValue(value);
                                                if (selectedKey) saveDraft(selectedKey, value);
                                            }}
                                            onClick={() => setIsEditing(true)}
                                            onKeyDown={handleTextareaKeyDown}
                                            placeholder="输入翻译..."
                                            rows={3}
                                        />
                                        <div className="proof-actions">
                                            <button className="btn-save" onClick={() => handleSave()}>✓ 保存并下一条</button>
                                            {selectedCategory !== "eventStory" && (
                                                <button className="btn-pinned" onClick={() => handleSave("pinned")}>🔒 锁定保存</button>
                                            )}
                                            <button className="btn-cancel" onClick={() => { setSelectedKey(null); setIsEditing(false); }}>取消</button>
                                            {detailInfo.mode === "multi" && detailInfo.ids && detailInfo.ids.length > 1 ? (
                                                <div className="detail-menu" ref={detailMenuRef}>
                                                    <button
                                                        className="btn-detail"
                                                        onClick={() => setDetailMenuOpen(v => !v)}
                                                        title="选择来源页面"
                                                    >
                                                        {detailInfo.label}
                                                    </button>
                                                    {detailMenuOpen && (
                                                        <div className="detail-menu-list">
                                                            {detailInfo.ids.map(id => (
                                                                <button
                                                                    key={id}
                                                                    className="detail-menu-item"
                                                                    onClick={() => {
                                                                        if (!detailInfo.builder) return;
                                                                        handleOpenDetail(detailInfo.builder(id));
                                                                        setLastDetailId(id);
                                                                        setDetailMenuOpen(false);
                                                                    }}
                                                                >
                                                                    {id}
                                                                </button>
                                                            ))}
                                                        </div>
                                                    )}
                                                </div>
                                            ) : (
                                                <button
                                                    className="btn-detail"
                                                    onClick={() => handleOpenDetail(detailInfo.url || "")}
                                                    disabled={!detailInfo.url}
                                                    title={detailInfo.url ? "打开来源详情" : detailInfo.disabledReason}
                                                >
                                                    {detailInfo.label}
                                                </button>
                                            )}
                                            <div className="proof-hints">
                                                <button
                                                    style={{ background: 'none', border: 'none', color: 'var(--accent)', cursor: 'pointer', fontSize: '0.75rem', marginRight: '0.5rem', padding: 0 }}
                                                    onClick={() => {
                                                        const next = saveShortcut === "enter" ? "shift-enter" : "enter";
                                                        setSaveShortcut(next);
                                                        localStorage.setItem("save-shortcut", next);
                                                    }}
                                                    title="切换快捷保存按钮配置"
                                                >
                                                    [切换快捷键]
                                                </button>
                                                {selectedCategory === "eventStory" && (
                                                    <button
                                                        style={{ background: 'none', border: 'none', color: 'var(--text-secondary)', cursor: 'pointer', fontSize: '0.75rem', marginRight: '0.5rem', padding: 0 }}
                                                        onClick={() => void handlePromoteCurrentEventStoryHuman()}
                                                        title="将当前活动剧情所有标题和台词标记为人工"
                                                    >
                                                        [整篇标记人工]
                                                    </button>
                                                )}
                                                保存: <kbd>{saveShortcut === "shift-enter" ? "Shift+Enter" : "Enter"}</kbd> &nbsp;
                                                <kbd>Ctrl/Cmd+↑↓</kbd> 切换 <kbd>Esc</kbd> 取消
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
                                                <th className="col-detail">页面</th>
                                                <th className="col-source">来源</th>
                                                <th className="col-jp">日文原文</th>
                                                <th className="col-cn">当前翻译</th>
                                            </tr>
                                        </thead>
                                        <tbody>
                                            {filteredEntries.map(entry => {
                                                const rowDetail = buildRowDetail(entry);
                                                return (
                                                    <tr
                                                        key={entry.key}
                                                        data-key={entry.key}
                                                        className={`entry-row ${selectedKey === entry.key ? "row-active" : ""}`}
                                                        onClick={() => selectEntry(entry.key)}
                                                    >
                                                        <td onClick={e => e.stopPropagation()}>
                                                            {rowDetail.mode === "multi" && rowDetail.ids ? (
                                                                <div className="detail-menu">
                                                                    <button
                                                                        className="btn-detail btn-detail-sm"
                                                                        onClick={() => setRowDetailMenuKey(k => (k === entry.key ? null : entry.key))}
                                                                    >
                                                                        {rowDetail.label}
                                                                    </button>
                                                                    {rowDetailMenuKey === entry.key && (
                                                                        <div className="detail-menu-list">
                                                                            {rowDetail.ids.map(id => (
                                                                                <button
                                                                                    key={id}
                                                                                    className="detail-menu-item"
                                                                                    onClick={() => {
                                                                                        if (!rowDetail.builder) return;
                                                                                        handleOpenDetail(rowDetail.builder(id));
                                                                                        setRowLastDetailId(prev => ({ ...prev, [entry.key]: id }));
                                                                                        setRowDetailMenuKey(null);
                                                                                    }}
                                                                                >
                                                                                    {id}
                                                                                </button>
                                                                            ))}
                                                                        </div>
                                                                    )}
                                                                </div>
                                                            ) : (
                                                                <button
                                                                    className="btn-detail btn-detail-sm"
                                                                    onClick={() => handleOpenDetail(rowDetail.url || "")}
                                                                    disabled={!rowDetail.url}
                                                                    title={rowDetail.url ? "打开来源详情" : (rowDetail.disabledReason || "缺少来源ID")}
                                                                >
                                                                    {rowDetail.label}
                                                                </button>
                                                            )}
                                                        </td>
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
                                                            {selectedCategory === "eventStory" ? getEventStoryEntryLabel(entry.key) : entry.key}
                                                            {selectedCategory === "eventStory" && (
                                                                <div style={{ fontSize: "0.75em", color: "var(--text-secondary)", marginTop: "4px" }}>
                                                                    第 {parseEventStoryEntryKey(entry.key).episodeNo} 章
                                                                </div>
                                                            )}
                                                        </div></td>
                                                        <td><div className="cn-text">{entry.text}</div></td>
                                                    </tr>
                                                );
                                            })}
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
