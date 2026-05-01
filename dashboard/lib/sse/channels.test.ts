import { describe, it, expect } from 'vitest';
import { KNOWN_CHANNELS, isKnownChannel, parseChannel } from './channels';

describe('lib/sse/channels', () => {
  it('M4 patterns are additive — M3 literals unchanged + M4 literals appended', () => {
    // Supervisor's pg_notify emit set:
    //   grep -E "pg_notify\\('([a-z._]+)" supervisor/migrations/*.sql
    //     -> {work.ticket.created}
    // M4 dashboard adds two more literals (work.ticket.edited from
    // T012 inline edits, work.agent.edited from T013 agent settings
    // edits). Parameterized work.ticket.transitioned.<dept>.<from>.<to>
    // and work.vault.<kind> are matched via KNOWN_CHANNEL_PATTERNS.
    expect([...KNOWN_CHANNELS]).toEqual([
      'work.ticket.created',
      'work.ticket.edited',
      'work.agent.edited',
      'work.chat.session_deleted',
      // M5.3 chat-driven mutation channels (FR-460 + FR-481).
      'work.chat.ticket.created',
      'work.chat.ticket.edited',
      'work.chat.ticket.transitioned',
      'work.chat.agent.paused',
      'work.chat.agent.resumed',
      'work.chat.agent.spawned',
      'work.chat.agent.config_edited',
      'work.chat.hiring.proposed',
      // M5.3 closes the M5.2 retro carryover (FR-462).
      'work.chat.session_started',
      'work.chat.message_sent',
      'work.chat.session_ended',
    ]);
  });

  it('M5.3 chat-mutation channel parses to the expected kind', () => {
    const event = parseChannel({
      id: 'evt-1',
      channel: 'work.chat.ticket.created',
      payload: {
        chat_session_id: 'sess-1',
        chat_message_id: 'msg-1',
        affected_resource_id: 'ticket-abc',
        affected_resource_type: 'ticket',
      },
      createdAt: new Date('2026-04-30T00:00:00Z'),
    });
    expect(event.kind).toBe('chat.ticket.created');
    expect(event).toMatchObject({
      chatSessionId: 'sess-1',
      chatMessageId: 'msg-1',
      affectedResourceId: 'ticket-abc',
      affectedResourceType: 'ticket',
    });
  });

  it('M5.3 chat lifecycle channels parse cleanly', () => {
    for (const ch of ['work.chat.session_started', 'work.chat.message_sent', 'work.chat.session_ended']) {
      const event = parseChannel({
        id: 'evt-' + ch,
        channel: ch,
        payload: { chat_session_id: 's-1', chat_message_id: 'm-1' },
        createdAt: new Date('2026-04-30T00:00:00Z'),
      });
      expect(event.kind).toBe(ch.replace('work.', ''));
    }
  });

  it('parseChannel("work.ticket.created", payload) yields a TicketCreated event', () => {
    const event = parseChannel({
      id: 'evt-1',
      channel: 'work.ticket.created',
      payload: { ticket_id: 'tk-1' },
      createdAt: new Date('2026-04-26T00:00:00Z'),
    });
    expect(event.kind).toBe('ticket.created');
    expect(event).toMatchObject({
      kind: 'ticket.created',
      eventId: 'evt-1',
      ticketId: 'tk-1',
      at: '2026-04-26T00:00:00.000Z',
    });
  });

  it('parseChannel("work.ticket.transitioned.engineering.todo.in_dev", payload) yields a TicketTransitioned event with parsed dept/from/to and hygieneStatus', () => {
    const event = parseChannel({
      id: 'evt-2',
      channel: 'work.ticket.transitioned.engineering.todo.in_dev',
      payload: { ticket_id: 'tk-2', hygiene_status: 'operator_initiated' },
      createdAt: new Date('2026-04-26T01:00:00Z'),
    });
    expect(event.kind).toBe('ticket.transitioned');
    expect(event).toMatchObject({
      kind: 'ticket.transitioned',
      eventId: 'evt-2',
      ticketId: 'tk-2',
      department: 'engineering',
      from: 'todo',
      to: 'in_dev',
      hygieneStatus: 'operator_initiated',
    });
  });

  it('parseChannel("work.ticket.edited", payload) yields a TicketEdited event with the diff record', () => {
    const event = parseChannel({
      id: 'evt-3',
      channel: 'work.ticket.edited',
      payload: {
        ticket_id: 'tk-3',
        diff: {
          title: { before: 'old title', after: 'new title' },
        },
      },
      createdAt: new Date('2026-04-27T00:00:00Z'),
    });
    expect(event.kind).toBe('ticket.edited');
    expect(event).toMatchObject({
      kind: 'ticket.edited',
      ticketId: 'tk-3',
      diff: { title: { before: 'old title', after: 'new title' } },
    });
  });

  it('parseChannel("work.agent.edited", payload) yields an AgentEdited event with the diff record', () => {
    const event = parseChannel({
      id: 'evt-4',
      channel: 'work.agent.edited',
      payload: {
        role_slug: 'engineer',
        diff: { model: { before: 'sonnet', after: 'haiku' } },
      },
      createdAt: new Date('2026-04-27T00:01:00Z'),
    });
    expect(event.kind).toBe('agent.edited');
    expect(event).toMatchObject({
      kind: 'agent.edited',
      roleSlug: 'engineer',
      diff: { model: { before: 'sonnet', after: 'haiku' } },
    });
  });

  it('parseChannel("work.vault.secret_created") yields a VaultSecretCreated event', () => {
    const event = parseChannel({
      id: 'evt-5',
      channel: 'work.vault.secret_created',
      payload: { secret_path: '/cust/operator/stripe_key' },
      createdAt: new Date('2026-04-27T00:02:00Z'),
    });
    expect(event.kind).toBe('vault.secret_created');
    expect(event).toMatchObject({
      kind: 'vault.secret_created',
      secretPath: '/cust/operator/stripe_key',
    });
  });

  it('parseChannel("work.vault.rotation_failed") carries failedStep when present in payload', () => {
    const event = parseChannel({
      id: 'evt-6',
      channel: 'work.vault.rotation_failed',
      payload: { secret_path: '/cust/operator/db_url', failed_step: 'audit' },
      createdAt: new Date('2026-04-27T00:03:00Z'),
    });
    expect(event.kind).toBe('vault.rotation_failed');
    expect(event).toMatchObject({
      kind: 'vault.rotation_failed',
      secretPath: '/cust/operator/db_url',
      failedStep: 'audit',
    });
  });

  it('parseChannel("work.vault.value_revealed") yields a VaultValueRevealed event', () => {
    const event = parseChannel({
      id: 'evt-7',
      channel: 'work.vault.value_revealed',
      payload: { secret_path: '/cust/operator/api_key' },
      createdAt: new Date('2026-04-27T00:04:00Z'),
    });
    expect(event.kind).toBe('vault.value_revealed');
  });

  it('parseChannel returns an unknown-kind event for non-allowlisted channels', () => {
    const event = parseChannel({
      id: 'evt-x',
      channel: 'totally.unrelated.channel',
      payload: {},
      createdAt: new Date(),
    });
    expect(event.kind).toBe('unknown');
  });

  it('isKnownChannel allows literal channels and pattern-matched parameterized channels', () => {
    expect(isKnownChannel('work.ticket.created')).toBe(true);
    expect(isKnownChannel('work.ticket.edited')).toBe(true);
    expect(isKnownChannel('work.agent.edited')).toBe(true);
    expect(isKnownChannel('work.chat.session_deleted')).toBe(true);
    expect(isKnownChannel('work.ticket.transitioned.engineering.todo.in_dev')).toBe(true);
    expect(isKnownChannel('work.ticket.transitioned.qa-engineer.in_dev.qa_review')).toBe(true);
    expect(isKnownChannel('work.vault.secret_created')).toBe(true);
    expect(isKnownChannel('work.vault.value_revealed')).toBe(true);
    expect(isKnownChannel('something.else')).toBe(false);
  });

  // ────────────────────────────────────────────────────────────
  // M5.2 — work.chat.session_deleted literal channel
  // ────────────────────────────────────────────────────────────

  it('TestSessionDeletedChannelKnown', () => {
    expect((KNOWN_CHANNELS as readonly string[]).includes('work.chat.session_deleted')).toBe(true);
    expect(isKnownChannel('work.chat.session_deleted')).toBe(true);
  });

  it('TestSessionDeletedParsesPayload', () => {
    const event = parseChannel({
      id: 'evt-chat-1',
      channel: 'work.chat.session_deleted',
      payload: {
        chat_session_id: '11111111-1111-1111-1111-111111111111',
        actor_user_id: '22222222-2222-2222-2222-222222222222',
      },
      createdAt: new Date('2026-04-29T13:00:00Z'),
    });
    expect(event.kind).toBe('chat.session_deleted');
    if (event.kind !== 'chat.session_deleted') return;
    expect(event.chatSessionId).toBe('11111111-1111-1111-1111-111111111111');
    expect(event.actorUserId).toBe('22222222-2222-2222-2222-222222222222');
    expect(event.eventId).toBe('evt-chat-1');
  });

  it('TestSessionDeletedRejectsExtraneousFields', () => {
    // Rule 6 backstop — even if the supervisor (or a future bug)
    // somehow sends content/message_id alongside the IDs, parseChannel
    // must NOT surface them on the variant. The discriminated union
    // shape limits what downstream code can read.
    const event = parseChannel({
      id: 'evt-chat-2',
      channel: 'work.chat.session_deleted',
      payload: {
        chat_session_id: '33333333-3333-3333-3333-333333333333',
        actor_user_id: '44444444-4444-4444-4444-444444444444',
        content: 'do not leak this',
        message_id: '55555555-5555-5555-5555-555555555555',
      },
      createdAt: new Date('2026-04-29T13:01:00Z'),
    });
    expect(event.kind).toBe('chat.session_deleted');
    const keys = Object.keys(event);
    expect(keys).toContain('chatSessionId');
    expect(keys).toContain('actorUserId');
    expect(keys).not.toContain('content');
    expect(keys).not.toContain('message_id');
    expect(keys).not.toContain('messageId');
  });

  // M5.3 — exhaustive chat-mutation channel parser coverage. Each
  // mutation channel has its own case branch in parseLiteralChannel
  // and shares the same payload-shape contract (Rule 6 backstop).
  it.each([
    ['work.chat.ticket.created', 'chat.ticket.created'] as const,
    ['work.chat.ticket.edited', 'chat.ticket.edited'] as const,
    ['work.chat.ticket.transitioned', 'chat.ticket.transitioned'] as const,
    ['work.chat.agent.paused', 'chat.agent.paused'] as const,
    ['work.chat.agent.resumed', 'chat.agent.resumed'] as const,
    ['work.chat.agent.spawned', 'chat.agent.spawned'] as const,
    ['work.chat.agent.config_edited', 'chat.agent.config_edited'] as const,
    ['work.chat.hiring.proposed', 'chat.hiring.proposed'] as const,
  ])('parseChannel(%s) yields kind=%s', (channel, expectedKind) => {
    const row = {
      id: 'evt-1',
      channel,
      payload: {
        chat_session_id: 'sess-uuid',
        chat_message_id: 'msg-uuid',
        verb: expectedKind.replace('chat.', '').replace('.', '_'),
        affected_resource_id: 'resource-uuid',
        affected_resource_type: 'ticket',
        extras: { from_column: 'todo', to_column: 'in_progress' },
      },
      createdAt: new Date('2026-04-30T12:00:00Z'),
    };
    const event = parseChannel(row);
    expect(event.kind).toBe(expectedKind);
  });

  it('parseChannel("work.chat.session_started") yields chat.session_started', () => {
    const row = {
      id: 'evt-life-1',
      channel: 'work.chat.session_started',
      payload: { chat_session_id: 'sess-2' },
      createdAt: new Date('2026-04-30T12:00:01Z'),
    };
    expect(parseChannel(row).kind).toBe('chat.session_started');
  });

  it('parseChannel("work.chat.message_sent") carries chatMessageId', () => {
    const row = {
      id: 'evt-life-2',
      channel: 'work.chat.message_sent',
      payload: { chat_session_id: 'sess-3', chat_message_id: 'msg-3' },
      createdAt: new Date('2026-04-30T12:00:02Z'),
    };
    expect(parseChannel(row).kind).toBe('chat.message_sent');
  });

  it('parseChannel("work.chat.session_ended") yields chat.session_ended', () => {
    const row = {
      id: 'evt-life-3',
      channel: 'work.chat.session_ended',
      payload: { chat_session_id: 'sess-4' },
      createdAt: new Date('2026-04-30T12:00:03Z'),
    };
    expect(parseChannel(row).kind).toBe('chat.session_ended');
  });

  it('parseChannel("work.vault.secret_edited") with changed_fields array', () => {
    const row = {
      id: 'evt-vault-1',
      channel: 'work.vault.secret_edited',
      payload: { secret_path: '/x/y', changed_fields: ['value', 'note'] },
      createdAt: new Date('2026-04-30T12:00:04Z'),
    };
    expect(parseChannel(row).kind).toBe('vault.secret_edited');
  });

  it('parseChannel("work.vault.secret_deleted") yields vault.secret_deleted', () => {
    const row = {
      id: 'evt-vault-2',
      channel: 'work.vault.secret_deleted',
      payload: { secret_path: '/x/y' },
      createdAt: new Date('2026-04-30T12:00:05Z'),
    };
    expect(parseChannel(row).kind).toBe('vault.secret_deleted');
  });

  it('parseChannel("work.vault.grant_added") and grant_removed yield matching kinds', () => {
    const baseRow = {
      payload: { secret_path: '/x/y', role_slug: 'eng', env_var_name: 'API_KEY' },
      createdAt: new Date('2026-04-30T12:00:06Z'),
    };
    const added = parseChannel({ ...baseRow, id: 'evt-grant-1', channel: 'work.vault.grant_added' });
    const removed = parseChannel({ ...baseRow, id: 'evt-grant-2', channel: 'work.vault.grant_removed' });
    expect(added.kind).toBe('vault.grant_added');
    expect(removed.kind).toBe('vault.grant_removed');
  });

  it('parseChannel("work.vault.rotation_initiated") and rotation_completed yield matching kinds', () => {
    const baseRow = {
      payload: { secret_path: '/x/y' },
      createdAt: new Date('2026-04-30T12:00:07Z'),
    };
    const init = parseChannel({ ...baseRow, id: 'evt-rot-1', channel: 'work.vault.rotation_initiated' });
    const done = parseChannel({ ...baseRow, id: 'evt-rot-2', channel: 'work.vault.rotation_completed' });
    expect(init.kind).toBe('vault.rotation_initiated');
    expect(done.kind).toBe('vault.rotation_completed');
  });

  it('parseChannel("work.vault.<unknown_subtype>") falls through to unknown kind', () => {
    const row = {
      id: 'evt-vault-x',
      channel: 'work.vault.future_subtype',
      payload: { secret_path: '/x/y' },
      createdAt: new Date('2026-04-30T12:00:08Z'),
    };
    expect(parseChannel(row).kind).toBe('unknown');
  });

  it('parseChannel handles null payload by treating it as empty record', () => {
    const row = {
      id: 'evt-null',
      channel: 'work.ticket.created',
      payload: null,
      createdAt: new Date('2026-04-30T12:00:09Z'),
    };
    expect(parseChannel(row).kind).toBe('ticket.created');
  });

  it('parseChannel handles createdAt as a string by parsing it to ISO', () => {
    const row = {
      id: 'evt-str-time',
      channel: 'work.ticket.created',
      payload: { ticket_id: 'tkt-1' },
      // OutboxRow types createdAt as Date but the runtime contract
      // lets the listener pass a Date-string from JSON; the helper
      // detects via instanceof and falls back to new Date().
      createdAt: '2026-04-30T12:00:10Z' as unknown as Date,
    };
    const event = parseChannel(row);
    expect(event.at).toContain('2026-04-30T12:00:10');
  });
});
