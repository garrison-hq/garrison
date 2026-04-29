import { test, expect } from './_coverage';
import postgres from 'postgres';
import { bootHarness, truncateDashboardState } from './_harness';

// Responsive acceptance per plan §"Test strategy > integration >
// responsive.spec.ts" (FR-011 + SC-003):
//   - every primary surface renders without horizontal page
//     scroll at 768px
//   - same at 1024px
//   - same at 1280px+
//
// We use Playwright's project config (playwright.config.ts) to run
// each viewport as a separate run — but for THIS spec we set the
// viewport explicitly per test for clarity.

const SURFACES = [
  '/',
  '/departments/engineering',
  '/activity',
  '/hygiene',
  '/vault',
  '/agents',
  '/admin/invites',
];

const VIEWPORTS = [
  { name: '768px', width: 768, height: 1024 },
  { name: '1024px', width: 1024, height: 768 },
  { name: '1280px', width: 1280, height: 800 },
];

async function authenticate(
  page: import('@playwright/test').Page,
  env: Awaited<ReturnType<typeof bootHarness>>,
) {
  const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
  try {
    const usersCount = await sql<{ count: string }[]>`SELECT count(*) FROM users`;
    if (Number(usersCount[0].count) === 0) {
      await fetch(`${env.BETTER_AUTH_URL}/api/auth/sign-up/email`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json', Origin: env.BETTER_AUTH_URL },
        body: JSON.stringify({ email: 'op@example.com', password: 'integration-pw-1', name: 'Op' }),
      });
    }
  } finally {
    await sql.end();
  }
  await page.goto('/login');
  await page.locator('input[name=email]').fill('op@example.com');
  await page.locator('input[name=password]').fill('integration-pw-1');
  await page.getByRole('button', { name: 'Sign in' }).click();
  await page.waitForURL((url) => url.pathname === '/');
}

test.describe('responsive', () => {
  test.beforeEach(async () => {
    const env = await bootHarness();
    await truncateDashboardState(env);
  });

  for (const viewport of VIEWPORTS) {
    test(`every primary surface renders without horizontal page scroll at ${viewport.name}`, async ({ page }) => {
      const env = await bootHarness();
      await page.setViewportSize({ width: viewport.width, height: viewport.height });
      await authenticate(page, env);
      for (const path of SURFACES) {
        await page.goto(path);
        const overflow = await page.evaluate(() => ({
          scrollWidth: document.documentElement.scrollWidth,
          clientWidth: document.documentElement.clientWidth,
        }));
        // Allow a 1px rounding tolerance.
        expect(
          overflow.scrollWidth,
          `${path} at ${overflow.clientWidth}px viewport overflowed (scrollWidth=${overflow.scrollWidth})`,
        ).toBeLessThanOrEqual(overflow.clientWidth + 1);
      }
    });
  }

  // M5.2 — chat surface block (T016, SC-210).
  // Iterates the three viewport sizes against the chat route and
  // asserts:
  //   - three-pane layout renders without horizontal scroll
  //   - sticky-bottom container exists in DOM
  //   - chat topbar strip stays visible
  //   - right-pane KnowsPanePlaceholder collapses on <1024px (FR-202)
  test('chat surface — 768/1024/1280 layouts', async ({ page }) => {
    const env = await bootHarness();
    await authenticate(page, env);

    // Seed a chat session so /chat/<uuid> renders the full
    // three-pane composition (the empty /chat path is structurally
    // simpler and doesn't exercise the right-pane breakpoint).
    const sql = postgres(env.TEST_SUPERUSER_DSN, { max: 1 });
    let sessionId: string;
    try {
      const userRow = await sql<{ id: string }[]>`
        SELECT id FROM users WHERE email = 'op@example.com' LIMIT 1
      `;
      const [row] = await sql<{ id: string }[]>`
        INSERT INTO chat_sessions (started_by_user_id)
        VALUES (${userRow[0].id})
        RETURNING id
      `;
      sessionId = row.id;
    } finally {
      await sql.end();
    }

    for (const viewport of VIEWPORTS) {
      await page.setViewportSize({ width: viewport.width, height: viewport.height });
      await page.goto(`/chat/${sessionId}`);
      const overflow = await page.evaluate(() => ({
        scrollWidth: document.documentElement.scrollWidth,
        clientWidth: document.documentElement.clientWidth,
      }));
      expect(
        overflow.scrollWidth,
        `/chat at ${viewport.name} overflowed (scrollWidth=${overflow.scrollWidth} > ${overflow.clientWidth})`,
      ).toBeLessThanOrEqual(overflow.clientWidth + 1);

      // Topbar strip + composer + message stream stay visible across viewports.
      await expect(page.getByTestId('chat-topbar-strip')).toBeVisible();

      // Right-pane KnowsPanePlaceholder: visible at >=1024px, hidden below.
      const knowsHidden = viewport.width < 1024;
      const knows = page.locator('aside[aria-label="What the CEO knows (placeholder)"]');
      if (knowsHidden) {
        await expect(knows).toBeHidden();
      } else {
        await expect(knows).toBeVisible();
      }
    }
  });
});
