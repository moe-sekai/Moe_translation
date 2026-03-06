/**
 * API client for the sekai-translate backend.
 * Much simpler than the old GitHub API approach — just REST calls to our Go server.
 */

// ============================================================================
// Types (shared with Go backend)
// ============================================================================

export interface FieldInfo {
    name: string;
    total: number;
    cnCount: number;
    humanCount: number;
    pinnedCount: number;
    llmCount: number;
    unknownCount: number;
}

export interface CategoryInfo {
    name: string;
    fields: FieldInfo[];
}

export interface TranslationEntry {
    key: string;
    text: string;
    source: string;
}

export interface PushStatus {
    lastPush: string;
    lastError: string;
    pushing: boolean;
}

// ============================================================================
// Auth — simple token stored in localStorage
// ============================================================================

export function getToken(): string | null {
    if (typeof window === "undefined") return null;
    return localStorage.getItem("translate-token");
}

export function setToken(token: string) {
    localStorage.setItem("translate-token", token);
}

export function clearToken() {
    localStorage.removeItem("translate-token");
    localStorage.removeItem("translate-username");
}

export function getUsername(): string {
    if (typeof window === "undefined") return "";
    return localStorage.getItem("translate-username") || "";
}

export function setUsername(name: string) {
    localStorage.setItem("translate-username", name);
}

// ============================================================================
// API fetch helper
// ============================================================================

const API_BASE = "/api";

export async function apiFetch<T>(path: string, options?: RequestInit): Promise<T> {
    const token = getToken();
    const res = await fetch(`${API_BASE}${path}`, {
        ...options,
        headers: {
            "Content-Type": "application/json",
            ...(token ? { Authorization: `Bearer ${token}` } : {}),
            ...options?.headers,
        },
    });

    if (res.status === 401) {
        clearToken();
        window.location.reload();
        throw new Error("Unauthorized");
    }

    if (!res.ok) {
        const err = await res.json().catch(() => ({ error: res.statusText }));
        throw new Error(err.error || res.statusText);
    }

    return res.json();
}

// ============================================================================
// API methods
// ============================================================================

export async function login(username: string, password: string) {
    return apiFetch<{ token: string; username: string }>("/login", {
        method: "POST",
        body: JSON.stringify({ username, password }),
    });
}

export async function getCategories() {
    return apiFetch<CategoryInfo[]>("/categories");
}

export async function getEntries(category: string, field: string, source?: string) {
    const params = new URLSearchParams({ category, field });
    if (source) params.set("source", source);
    return apiFetch<TranslationEntry[]>(`/entries?${params}`);
}

export async function updateEntry(category: string, field: string, key: string, text: string, source: string) {
    return apiFetch<{ status: string }>("/entry", {
        method: "PUT",
        body: JSON.stringify({ category, field, key, text, source }),
    });
}

export async function pushToHub() {
    return apiFetch<{ status: string }>("/push", { method: "POST" });
}

export async function getPushStatus() {
    return apiFetch<PushStatus>("/status");
}
