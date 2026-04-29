// Non-server-action exports for chat — types + the ChatError class.
// Lives outside chat.ts because the dashboard's 'use server' file may
// only export async functions per Next 16.2.4 / Turbopack's strict
// validation. Server-action callers + client components both import
// from here.

import type { chatSessions } from '@/drizzle/schema.supervisor';

export const ChatErrorKind = {
  EmptyContent: 'empty_content',
  ContentTooLarge: 'content_too_large',
  SessionEnded: 'session_ended',
  SessionNotFound: 'session_not_found',
  TurnIndexCollision: 'turn_index_collision',
} as const;
export type ChatErrorKind = (typeof ChatErrorKind)[keyof typeof ChatErrorKind];

export class ChatError extends Error {
  constructor(public readonly kind: ChatErrorKind, message?: string) {
    super(message ?? kind);
    this.name = 'ChatError';
  }
}

export type ChatSessionRow = typeof chatSessions.$inferSelect;

export interface RecentThreadRow {
  id: string;
  startedAt: string;
  threadNumber: number;
  status: string;
  isArchived: boolean;
}
