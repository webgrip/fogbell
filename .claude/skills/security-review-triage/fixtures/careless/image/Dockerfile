FROM node:24.22.2-alpine@sha256:8a32c8cf649a49915a66e81e7a609223c708d0adabf288ee3f4077d765542f85
ARG NPM_TOKEN
ADD https://example.invalid/tool.tar.gz /opt/tool.tar.gz
RUN curl -fsSL https://example.invalid/install.sh | sh
COPY . /app
CMD ["node", "/app/server.js"]
