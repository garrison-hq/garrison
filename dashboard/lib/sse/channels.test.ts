import { describe, it, expect } from 'vitest';
import { KNOWN_CHANNELS, isKnownChannel, parseChannel } from './channels';

describe('lib/sse/channels', () => {
  it('KNOWN_CHANNELS list matches the supervisor emit set at plan-time', () => {
    // The supervisor's pg_notify emit set per
    //   grep -E "pg_notify\\('([a-z._]+)" supervisor/migrations/*.sql
    // is exactly {work.ticket.created} for literal channels.
    // (Parameterized work.ticket.transitioned.<dept>.<from>.<to>
    // is matched via KNOWN_CHANNEL_PATTERNS, not LISTEN.)
    expect([...KNOWN_CHANNELS]).toEqual(['work.ticket.created']);
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

  it('parseChannel("work.ticket.transitioned.engineering.todo.in_dev", payload) yields a TicketTransitioned event with parsed dept/from/to', () => {
    const event = parseChannel({
      id: 'evt-2',
      channel: 'work.ticket.transitioned.engineering.todo.in_dev',
      payload: { ticket_id: 'tk-2' },
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
    });
  });

  it('parseChannel returns an unknown-kind event for non-allowlisted channels', () => {
    const event = parseChannel({
      id: 'evt-3',
      channel: 'totally.unrelated.channel',
      payload: {},
      createdAt: new Date(),
    });
    expect(event.kind).toBe('unknown');
  });

  it('isKnownChannel allows literal channels and pattern-matched parameterized channels', () => {
    expect(isKnownChannel('work.ticket.created')).toBe(true);
    expect(isKnownChannel('work.ticket.transitioned.engineering.todo.in_dev')).toBe(true);
    expect(isKnownChannel('work.ticket.transitioned.qa-engineer.in_dev.qa_review')).toBe(true);
    expect(isKnownChannel('something.else')).toBe(false);
  });
});
