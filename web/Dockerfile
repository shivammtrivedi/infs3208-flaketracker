# The dashboard image: build the React app with Node, then serve the static
# files with nginx. Build context is the web/ folder:
#   docker build -t flaketracker/web web/

# ---------- stage 1: build the static site ----------
FROM node:22-alpine AS build
WORKDIR /app

# Install dependencies first so this step is cached until package*.json change.
# `npm ci` installs exactly what package-lock.json says (reproducible builds).
COPY package.json package-lock.json ./
RUN npm ci

COPY . .
# tsc type-checks, then vite writes the production bundle to /app/dist.
RUN npm run build

# ---------- stage 2: serve it ----------
# nginx-unprivileged runs as a non-root user and listens on 8080 (not 80).
FROM nginxinc/nginx-unprivileged:1.28-alpine
COPY nginx.conf /etc/nginx/conf.d/default.conf
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 8080
