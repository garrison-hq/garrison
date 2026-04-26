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

# Copy the rest and build.
COPY . .
RUN bun run build

# stage 2: runtime
FROM node:22-alpine AS runtime
WORKDIR /app
RUN addgroup -S dashboard && adduser -S dashboard -G dashboard

# next.config.ts has output: 'standalone' — copy the standalone
# bundle, the generated static assets, and the public folder.
# Standalone bundles its own minimal node_modules.
COPY --from=build --chown=dashboard:dashboard /app/.next/standalone ./
COPY --from=build --chown=dashboard:dashboard /app/.next/static ./.next/static
COPY --from=build --chown=dashboard:dashboard /app/public ./public

USER dashboard
EXPOSE 3000
ENV NODE_ENV=production
ENV HOSTNAME=0.0.0.0
CMD ["node", "server.js"]
