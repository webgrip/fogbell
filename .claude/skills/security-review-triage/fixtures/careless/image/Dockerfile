FROM node:24.22.3-alpine@sha256:116b800c3754b440ee0aa3ed2f20f5bf3d99a554bfd9adeea09ea8017f6e85f5
ARG NPM_TOKEN
ADD https://example.invalid/tool.tar.gz /opt/tool.tar.gz
RUN curl -fsSL https://example.invalid/install.sh | sh
COPY . /app
CMD ["node", "/app/server.js"]
