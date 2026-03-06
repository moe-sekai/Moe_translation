import type { Metadata } from "next";
import { ThemeProvider } from "./providers";
import "./globals.css";

export const metadata: Metadata = {
    title: "翻译校对 - Moesekai",
    description: "Moesekai Translation Proofreading Tool",
    robots: { index: false, follow: false },
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
    return (
        <html lang="zh-CN" suppressHydrationWarning>
            <body>
                <ThemeProvider
                    attribute="class"
                    defaultTheme="system"
                    enableSystem
                    disableTransitionOnChange
                >
                    {children}
                </ThemeProvider>
            </body>
        </html>
    );
}
