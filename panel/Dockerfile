# Build context: panel/   (docker build -t fl-panel:dev panel)
FROM node:24-alpine AS build
WORKDIR /app
COPY package.json package-lock.json ./
RUN npm ci
COPY . .
# tsc --noEmit (strict) runs first, so a type error fails the image build.
RUN npm run build

# nginx-unprivileged: runs as a non-root user and listens on 8080.
FROM nginxinc/nginx-unprivileged:1.29-alpine
# Fills in the resolver from /etc/resolv.conf so the gateway's address is looked up at request time,
# not once at startup (the panel may start before the gateway exists).
ENV NGINX_ENTRYPOINT_LOCAL_RESOLVERS=1
ENV GATEWAY_UPSTREAM=http://gateway:8080
COPY nginx.conf.template /etc/nginx/templates/default.conf.template
COPY --from=build /app/dist /usr/share/nginx/html
EXPOSE 8080
