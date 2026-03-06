import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
    title: "翻译校对 - Moesekai",
    description: "Moesekai Translation Proofreading Tool",
    robots: { index: false, follow: false },
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
    return (
        <html lang="zh-CN">
            <body>{children}</body>
        </html>
    );
}
