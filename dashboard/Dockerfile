# M3 production Dockerfile (FR-014).
# Multi-stage; runtime image based on node:22-alpine.
# Per CL-004 / plan §"Open questions for plan execution": drizzle-kit
# is NOT included in the runtime image — operators run migrations
# from a separate ops shell.

# stage 1: build
FROM oven/bun:1-alpine AS build
WORKDIR /app

# Install deps first to keep this layer cacheable across source edits.
COPY package.json bun.lock ./
RUN bun install --frozen-lockfile

# Copy build inputs only — explicit list keeps secrets, .env files,
# .git, IDE state, and anything else a recursive copy would sweep
# in out of the build context. .dockerignore is the second line of
# defence; this is the first.
COPY app ./app
COPY components ./components
COPY drizzle ./drizzle
COPY lib ./lib
COPY messages ./messages
COPY public ./public
COPY scripts ./scripts
COPY drizzle.config.ts drizzle.pull.config.ts ./
COPY middleware.ts next.config.ts postcss.config.js tailwind.config.ts tsconfig.json ./
RUN bun run build

# stage 2: runtime
FROM node:22-alpine AS runtime
WORKDIR /app
RUN addgroup -S dashboard && adduser -S dashboard -G dashboard

# next.config.ts has output: 'standalone' — copy the standalone
# bundle, the generated static assets, and the public folder.
# Standalone bundles its own minimal node_modules.
#
# --chmod=550 / 444: dashboard owns the files for read+exec but
# can't write them — the runtime never modifies code or assets.
# Combined with the non-root USER below, this satisfies docker:S6504
# (no write permissions on copied resources).
COPY --from=build --chown=dashboard:dashboard --chmod=550 /app/.next/standalone ./
COPY --from=build --chown=dashboard:dashboard --chmod=444 /app/.next/static ./.next/static
COPY --from=build --chown=dashboard:dashboard --chmod=444 /app/public ./public

USER dashboard
EXPOSE 3000
ENV NODE_ENV=production
ENV HOSTNAME=0.0.0.0
CMD ["node", "server.js"]
