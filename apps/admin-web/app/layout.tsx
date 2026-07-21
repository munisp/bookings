import type { Metadata } from "next";
import { ToastProvider } from "@/components/ui/toast";
import "./globals.css";

export const metadata: Metadata = {
  title: {
    default: "OpenDesk — Open-source AI receptionist",
    template: "%s · OpenDesk",
  },
  description:
    "OpenDesk is the open-source, multi-tenant AI receptionist platform: voice + text concierge, bookings, catalog, knowledge base and payments.",
};

export default function RootLayout({
  children,
}: Readonly<{ children: React.ReactNode }>) {
  return (
    <html lang="en">
      <body>
        <ToastProvider>{children}</ToastProvider>
      </body>
    </html>
  );
}
