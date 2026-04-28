// TypeScript port of supervisor/internal/vault/testutil.go.
//
// Boots a three-container Infisical stack (Postgres 15 + Redis 7 +
// Infisical v0.159.22) on a shared Docker bridge network, runs the
// admin bootstrap, and returns a harness that can mint Universal-Auth
// machine identities for the dashboard process to authenticate as.
//
// Used by the Playwright integration harness (_harness.ts) so vault
// server actions exercise a real Infisical instance instead of the
// soft-default "configure vault" prompt path.
//
// Image and bootstrap are kept in lockstep with the Go-side
// supervisor/internal/vault/testutil.go — same image tag, same admin
// signup → select-organization → create project flow, same UA setup.
// If you change one, change both.

import {
  GenericContainer,
  Network,
  Wait,
  type StartedTestContainer,
  type StartedNetwork,
} from 'testcontainers';

const INFISICAL_IMAGE = 'infisical/infisical:v0.159.22';
const PG_IMAGE = 'postgres:15-alpine';
const REDIS_IMAGE = 'redis:7-alpine';

const INFISICAL_PG_ALIAS = 'infisical-postgres';
const INFISICAL_REDIS_ALIAS = 'infisical-redis';
const INFISICAL_SERVER_ALIAS = 'infisical-server';

const ADMIN_EMAIL = 'admin@garrison.test';
const ADMIN_PASSWORD = 'Garrison-Test-Password-2026!';

const UA_API_PATH = '/api/v1/auth/universal-auth/identities/';
const TRUSTED_IP_ALL = '0.0.0.0/0';

export interface InfisicalCredentials {
  /** Site URL the dashboard should use (host-mapped). */
  siteUrl: string;
  /** Project (workspace) UUID created during bootstrap. */
  projectId: string;
  /** Environment slug — always 'dev' for the test workspace. */
  environment: string;
  /** Universal-Auth client ID for a freshly-minted machine identity. */
  clientId: string;
  /** Universal-Auth client secret for the same identity. */
  clientSecret: string;
}

export class InfisicalTestHarness {
  private constructor(
    public readonly siteUrl: string,
    private readonly orgToken: string,
    private readonly orgId: string,
    public readonly projectId: string,
    private readonly network: StartedNetwork,
    private readonly pgC: StartedTestContainer,
    private readonly redisC: StartedTestContainer,
    private readonly infisicalC: StartedTestContainer,
  ) {}

  static async start(): Promise<InfisicalTestHarness> {
    const network = await new Network().start();

    const pgC = await new GenericContainer(PG_IMAGE)
      .withNetwork(network)
      .withNetworkAliases(INFISICAL_PG_ALIAS)
      .withEnvironment({
        POSTGRES_DB: 'infisical',
        POSTGRES_USER: 'infisical',
        POSTGRES_PASSWORD: 'infisical-test-pw',
      })
      .withWaitStrategy(
        Wait.forLogMessage(/database system is ready to accept connections/, 2),
      )
      .withStartupTimeout(90_000)
      .start();

    let redisC: StartedTestContainer;
    try {
      redisC = await new GenericContainer(REDIS_IMAGE)
        .withNetwork(network)
        .withNetworkAliases(INFISICAL_REDIS_ALIAS)
        .withWaitStrategy(Wait.forLogMessage(/Ready to accept connections/))
        .withStartupTimeout(60_000)
        .start();
    } catch (err) {
      await pgC.stop();
      await network.stop();
      throw err;
    }

    let infisicalC: StartedTestContainer;
    try {
      infisicalC = await new GenericContainer(INFISICAL_IMAGE)
        .withNetwork(network)
        .withNetworkAliases(INFISICAL_SERVER_ALIAS)
        .withExposedPorts(8080)
        .withEnvironment({
          ENCRYPTION_KEY: '6c1fe4e407b8911c104518103505b218',
          AUTH_SECRET: 'JpRi1OB18JFjFlNXj+j9USjFiMPXBimW7EJNzS4/b8s=',
          DB_CONNECTION_URI: `postgresql://infisical:infisical-test-pw@${INFISICAL_PG_ALIAS}:5432/infisical`,
          REDIS_URL: `redis://${INFISICAL_REDIS_ALIAS}:6379`,
          HTTPS_ENABLED: 'false',
          TELEMETRY_ENABLED: 'false',
          SITE_URL: 'http://localhost:8080',
          DISABLE_SECRET_SCANNING: 'true',
          NODE_ENV: 'development',
        })
        .withWaitStrategy(
          Wait.forHttp('/api/status', 8080).forStatusCodeMatching((s) => s < 500),
        )
        .withStartupTimeout(300_000)
        .start();
    } catch (err) {
      await redisC.stop();
      await pgC.stop();
      await network.stop();
      throw err;
    }

    const host = infisicalC.getHost();
    const port = infisicalC.getMappedPort(8080);
    const siteUrl = `http://${host}:${port}`;

    const { orgToken, orgId, projectId } = await bootstrap(siteUrl);

    return new InfisicalTestHarness(
      siteUrl,
      orgToken,
      orgId,
      projectId,
      network,
      pgC,
      redisC,
      infisicalC,
    );
  }

  /**
   * Mint a Universal-Auth machine identity, configure access policy,
   * create a client secret, and grant project admin access. Returns
   * the credentials the dashboard process can use to authenticate
   * via INFISICAL_DASHBOARD_ML_CLIENT_ID + _SECRET.
   */
  async createMachineIdentity(name: string): Promise<{ clientId: string; clientSecret: string }> {
    const identityId = await this.createIdentity(name);
    const clientId = await this.configureUniversalAuth(identityId);
    const clientSecret = await this.createClientSecret(identityId);
    await this.addIdentityToProject(identityId);
    return { clientId, clientSecret };
  }

  /** Convenience: produce a full credential bundle for the dashboard env. */
  async issueCredentials(name = 'garrison-dashboard-test'): Promise<InfisicalCredentials> {
    const { clientId, clientSecret } = await this.createMachineIdentity(name);
    return {
      siteUrl: this.siteUrl,
      projectId: this.projectId,
      environment: 'dev',
      clientId,
      clientSecret,
    };
  }

  /**
   * Idempotently create each segment of `folderPath` under the test
   * project's "dev" environment. Mirrors the supervisor harness's
   * ensureFolders helper (testutil.go). Required before
   * dashboard's createSecret because Infisical does not auto-
   * create folder hierarchies on POST /secrets/raw.
   */
  async ensureFolder(folderPath: string): Promise<void> {
    const trimmed = folderPath.replace(/^\/+|\/+$/g, '');
    if (trimmed === '') return;
    const segments = trimmed.split('/');
    let parent = '/';
    for (const seg of segments) {
      const body = {
        workspaceId: this.projectId,
        environment: 'dev',
        name: seg,
        path: parent,
      };
      const res = await fetch(`${this.siteUrl}/api/v1/folders`, {
        method: 'POST',
        headers: {
          'Content-Type': 'application/json',
          Authorization: `Bearer ${this.orgToken}`,
        },
        body: JSON.stringify(body),
      });
      if (!res.ok) {
        const text = await res.text().catch(() => '');
        const lower = text.toLowerCase();
        // Idempotent: 400 + "already exist" → existing folder, fine.
        if (!(res.status === 400 && lower.includes('already exist'))) {
          throw new Error(
            `ensureFolder ${parent}/${seg}: HTTP ${res.status}: ${text}`,
          );
        }
      }
      parent = parent === '/' ? `/${seg}` : `${parent}/${seg}`;
    }
  }

  async stop(): Promise<void> {
    try {
      await this.infisicalC.stop();
    } catch {
      // ignore
    }
    try {
      await this.redisC.stop();
    } catch {
      // ignore
    }
    try {
      await this.pgC.stop();
    } catch {
      // ignore
    }
    try {
      await this.network.stop();
    } catch {
      // ignore
    }
  }

  // ─── Internal API helpers ───────────────────────────────────

  private async createIdentity(name: string): Promise<string> {
    const body = { name, organizationId: this.orgId, role: 'member' };
    const json = await callJson(
      this.siteUrl,
      'POST',
      '/api/v1/identities',
      body,
      this.orgToken,
    ) as { identity: { id: string } };
    return json.identity.id;
  }

  private async configureUniversalAuth(identityId: string): Promise<string> {
    const body = {
      clientSecretTrustedIps: [{ ipAddress: TRUSTED_IP_ALL }],
      accessTokenTrustedIps: [{ ipAddress: TRUSTED_IP_ALL }],
      accessTokenTTL: 86400,
      accessTokenMaxTTL: 2592000,
      accessTokenNumUsesLimit: 0,
    };
    const json = await callJson(
      this.siteUrl,
      'POST',
      `${UA_API_PATH}${identityId}`,
      body,
      this.orgToken,
    ) as { identityUniversalAuth: { clientId: string } };
    return json.identityUniversalAuth.clientId;
  }

  private async createClientSecret(identityId: string): Promise<string> {
    const body = { description: 'garrison-test', numUsesLimit: 0, ttl: 0 };
    const json = await callJson(
      this.siteUrl,
      'POST',
      `${UA_API_PATH}${identityId}/client-secrets`,
      body,
      this.orgToken,
    ) as { clientSecret: string };
    return json.clientSecret;
  }

  private async addIdentityToProject(identityId: string): Promise<void> {
    await callJson(
      this.siteUrl,
      'POST',
      `/api/v1/projects/${this.projectId}/memberships/identities/${identityId}`,
      { role: 'admin' },
      this.orgToken,
    );
  }
}

async function bootstrap(
  siteUrl: string,
): Promise<{ orgToken: string; orgId: string; projectId: string }> {
  const signup = await callJson(
    siteUrl,
    'POST',
    '/api/v1/admin/signup',
    {
      firstName: 'Test',
      lastName: 'Admin',
      email: ADMIN_EMAIL,
      password: ADMIN_PASSWORD,
    },
    '',
  ) as { token: string; organization: { id: string } };

  const orgSelect = await callJson(
    siteUrl,
    'POST',
    '/api/v3/auth/select-organization',
    { organizationId: signup.organization.id },
    signup.token,
  ) as { token: string };

  const project = await callJson(
    siteUrl,
    'POST',
    '/api/v1/projects',
    { projectName: 'garrison-test' },
    orgSelect.token,
  ) as { project: { id: string } };

  return {
    orgToken: orgSelect.token,
    orgId: signup.organization.id,
    projectId: project.project.id,
  };
}

async function callJson(
  siteUrl: string,
  method: string,
  path: string,
  body: unknown,
  token: string,
): Promise<unknown> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' };
  if (token) headers.Authorization = `Bearer ${token}`;
  const res = await fetch(`${siteUrl}${path}`, {
    method,
    headers,
    body: body === null ? undefined : JSON.stringify(body),
  });
  if (!res.ok) {
    const text = await res.text().catch(() => '');
    throw new Error(`Infisical ${method} ${path}: HTTP ${res.status}: ${text}`);
  }
  return res.json();
}
