FROM node:24-alpine
ARG NPM_TOKEN
ADD https://example.invalid/tool.tar.gz /opt/tool.tar.gz
RUN curl -fsSL https://example.invalid/install.sh | sh
COPY . /app
CMD ["node", "/app/server.js"]
