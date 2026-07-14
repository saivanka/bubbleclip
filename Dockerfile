FROM node:20-alpine

WORKDIR /app

COPY package.json ./
RUN npm install --omit=dev

COPY server.js ./
COPY public ./public

# persisted clipboard history + access code, owned by the unprivileged user
RUN mkdir -p /app/data && chown -R node:node /app
VOLUME ["/app/data"]

USER node

ENV PORT=5678
EXPOSE 5678

HEALTHCHECK --interval=30s --timeout=3s \
  CMD wget -qO- http://localhost:5678/api/health || exit 1

CMD ["node", "server.js"]
