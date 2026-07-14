FROM node:22 AS clone
ARG VOCDONI_APP_REF=develop
RUN apt-get update && apt-get install --no-install-recommends -y git && rm -rf /var/lib/apt/lists/*
WORKDIR /repo
RUN git clone --depth 1 --branch "${VOCDONI_APP_REF}" https://github.com/vocdoni/vocdoni-app.git .

FROM node:22
WORKDIR /app
RUN corepack enable && corepack prepare pnpm@10.16.1 --activate
COPY --from=clone /repo/package.json /repo/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile --ignore-scripts
COPY --from=clone /repo/ .

CMD ["pnpm", "start"]
